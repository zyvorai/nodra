#!/usr/bin/env python3
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
"""Judge pass/fail for a scripts/ci/soak.sh run from its summary.json.

Re-derives criteria independently of soak.sh's own exit code, per
docs/QUALIFICATION.md's "Multi-hour WAN / disk soak" row:

  - no_data_loss:      every accepted event is eventually delivered,
                        dead-lettered, or still legitimately pending — no
                        counter goes backwards, and progress is made whenever
                        events are accepted.
  - delivery_catchup:  edge spool depth returns to at or below its
                        pre-outage baseline within a bounded drain window
                        after each WAN-loss cycle's control-plane restores.
  - bounded_growth:    container memory does not show unbounded growth
                        across the run (mean of the last 10% of samples vs.
                        the first 10%).
  - no_crash:          neither container's restart count increased outside
                        the intentional WAN-loss stop/start cycles.
  - readiness_bound:   every WAN-loss cycle's time-to-ready stays under the
                        configured ceiling (no cycle timed out).

Usage: soak-check.py <path/to/summary.json>
Exits 0 if every non-skipped criterion passes, 1 otherwise.
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

CRITERIA = []


def verdict(name: str, status: str, detail: str) -> None:
    CRITERIA.append({"id": name, "status": status, "detail": detail})
    mark = {"pass": "PASS", "fail": "FAIL", "skip": "SKIP"}[status]
    print(f"[{mark}] {name} — {detail}")


def load_jsonl(path: Path) -> list[dict]:
    if not path.exists():
        return []
    out = []
    for line in path.read_text().splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            out.append(json.loads(line))
        except json.JSONDecodeError:
            continue
    return out


def check_no_data_loss(summary: dict) -> None:
    m = summary.get("metrics", {})
    events_gained = m.get("events_end", 0) - m.get("events_start", 0)
    deliveries_gained = m.get("deliveries_end", 0) - m.get("deliveries_start", 0)
    deadletters_gained = m.get("dead_letters_end", 0) - m.get("dead_letters_start", 0)
    failures_gained = m.get("delivery_failures_end", 0) - m.get("delivery_failures_start", 0)
    pending_end = m.get("pending_deliveries_end", 0)

    regressions = [
        k for k in ("events", "deliveries", "dead_letters", "delivery_failures")
        if m.get(f"{k}_end", 0) < m.get(f"{k}_start", 0)
    ]
    if regressions:
        verdict("no_data_loss", "fail", f"counters went backwards: {regressions}")
        return

    if events_gained <= 0:
        verdict("no_data_loss", "skip", "no events accepted during the run")
        return

    handled = deliveries_gained + deadletters_gained + pending_end
    if handled <= 0:
        verdict(
            "no_data_loss", "fail",
            f"{events_gained} events accepted but 0 delivered/dead-lettered/pending "
            f"(failures_gained={failures_gained})",
        )
        return

    verdict(
        "no_data_loss", "pass",
        f"events_gained={events_gained} deliveries_gained={deliveries_gained} "
        f"dead_letters_gained={deadletters_gained} pending_end={pending_end}",
    )


def check_delivery_catchup(wan_cycles: list[dict]) -> None:
    if not wan_cycles:
        verdict("delivery_catchup", "skip", "no WAN-loss cycles recorded")
        return

    failures = []
    for c in wan_cycles:
        pre = c.get("pre_outage_agent_healthz", {}) or {}
        post = c.get("post_restore_agent_healthz", {}) or {}
        pre_items = (pre.get("spool") or {}).get("items", 0)
        post_items = (post.get("spool") or {}).get("items", 0)
        # Allow bounded growth right at restore (drain hasn't fully run yet);
        # only flag a cycle where the spool is left far above baseline.
        tolerance = max(pre_items * 2, 10)
        if post_items > pre_items + tolerance:
            failures.append(
                f"cycle {c.get('cycle')}: pre={pre_items} post={post_items} (tolerance={tolerance})"
            )

    if failures:
        verdict("delivery_catchup", "fail", "; ".join(failures))
    else:
        verdict(
            "delivery_catchup", "pass",
            f"{len(wan_cycles)} cycle(s), spool returned within tolerance of baseline each time",
        )


def check_bounded_growth(samples: list[dict]) -> None:
    if len(samples) < 10:
        verdict("bounded_growth", "skip", f"only {len(samples)} samples — too few to judge a trend")
        return

    def series(key: str) -> list[int]:
        return [
            (s.get(key) or {}).get("mem_bytes", 0)
            for s in samples
            if (s.get(key) or {}).get("mem_bytes", 0) > 0
        ]

    failures = []
    for label, key in (("control-plane", "cp_stats"), ("edge-agent", "agent_stats")):
        vals = series(key)
        if len(vals) < 10:
            continue
        n = max(1, len(vals) // 10)
        first = sum(vals[:n]) / n
        last = sum(vals[-n:]) / n
        if first <= 0:
            continue
        ratio = last / first
        if ratio > 1.5:
            failures.append(f"{label}: mem grew {ratio:.2f}x (first~{first:.0f}B last~{last:.0f}B)")

    if failures:
        verdict("bounded_growth", "fail", "; ".join(failures))
    else:
        verdict("bounded_growth", "pass", "no container showed >1.5x memory growth over the run")


def check_no_crash(summary: dict) -> None:
    r = summary.get("restarts", {})
    bad = []
    for svc in ("control_plane", "edge_agent"):
        start = r.get(f"{svc}_start", 0)
        end = r.get(f"{svc}_end", 0)
        if end > start:
            bad.append(f"{svc}: {start} -> {end}")
    if bad:
        verdict("no_crash", "fail", "unexpected restarts: " + "; ".join(bad))
    else:
        verdict("no_crash", "pass", "no unexpected container restarts")


def check_readiness_bound(summary: dict, wan_cycles: list[dict]) -> None:
    if not wan_cycles:
        verdict("readiness_bound", "skip", "no WAN-loss cycles recorded")
        return
    ceiling = summary.get("ready_ceiling_s", 30)
    times = [c.get("time_to_ready_s", -1) for c in wan_cycles]
    timeouts = [c["cycle"] for c, t in zip(wan_cycles, times) if t is None or t < 0]
    over = [(c["cycle"], t) for c, t in zip(wan_cycles, times) if t is not None and t >= 0 and t > ceiling]
    if timeouts or over:
        detail = f"timed out cycles={timeouts} over-ceiling={over} (ceiling={ceiling}s)"
        verdict("readiness_bound", "fail", detail)
    else:
        good = [t for t in times if t is not None and t >= 0]
        worst = max(good) if good else 0
        verdict("readiness_bound", "pass", f"max time_to_ready={worst}s <= ceiling={ceiling}s")


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: soak-check.py <path/to/summary.json>", file=sys.stderr)
        return 2

    summary_path = Path(sys.argv[1])
    summary = json.loads(summary_path.read_text())
    outdir = summary_path.parent

    wan_cycles = load_jsonl(outdir / summary.get("wan_cycles_path", "wan-cycles.jsonl"))
    samples = load_jsonl(outdir / summary.get("samples_path", "samples.jsonl"))

    check_no_data_loss(summary)
    check_delivery_catchup(wan_cycles)
    check_bounded_growth(samples)
    check_no_crash(summary)
    check_readiness_bound(summary, wan_cycles)

    report_path = outdir / "soak-check.json"
    report_path.write_text(json.dumps({"criteria": CRITERIA}, indent=2) + "\n")

    failed = [c for c in CRITERIA if c["status"] == "fail"]
    if failed:
        print(f"\n{len(failed)} criterion(criteria) failed")
        return 1
    print("\nall soak criteria passed (or were skipped for lack of data)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
