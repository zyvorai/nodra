#!/usr/bin/env python3
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
"""Measure a short local HTTP publish envelope against a running edge agent.

This prints observed accepts/sec for the configured duration. It is not a
certified rating and must not be quoted as one without the lab host, duration,
commit, and worker count recorded in the output.

Usage:
  NODRA_BENCH_URL=http://127.0.0.1:9091 \\
  NODRA_BENCH_TOKEN=nodra-demo-local \\
  NODRA_BENCH_SECONDS=30 \\
  NODRA_BENCH_WORKERS=1 \\
  python3 scripts/bench-ingress.py
"""

from __future__ import annotations

import json
import os
import sys
import threading
import time
import urllib.error
import urllib.request


def main() -> int:
    url = os.environ.get("NODRA_BENCH_URL", "http://127.0.0.1:9091").rstrip("/") + "/v1/publish"
    token = os.environ.get("NODRA_BENCH_TOKEN", "nodra-demo-local")
    seconds = float(os.environ.get("NODRA_BENCH_SECONDS", "30"))
    workers = max(1, int(os.environ.get("NODRA_BENCH_WORKERS", "1")))
    topic = os.environ.get("NODRA_BENCH_TOPIC", "bench/ingress")
    deadline = time.monotonic() + seconds
    ok = fail = 0
    lock = threading.Lock()
    stop = threading.Event()

    def worker(wid: int) -> None:
        nonlocal ok, fail
        n = 0
        while not stop.is_set() and time.monotonic() < deadline:
            body = json.dumps({"topic": topic, "payload": {"n": n, "w": wid, "t": time.time()}}).encode()
            n += 1
            req = urllib.request.Request(
                url,
                data=body,
                headers={
                    "Authorization": f"Bearer {token}",
                    "Content-Type": "application/json",
                },
                method="POST",
            )
            try:
                with urllib.request.urlopen(req, timeout=5) as resp:
                    good = 200 <= resp.status < 300
            except urllib.error.HTTPError as e:
                good = False
                if e.code == 401:
                    stop.set()
            except Exception:
                good = False
            with lock:
                if good:
                    ok += 1
                else:
                    fail += 1

    t0 = time.monotonic()
    threads = [threading.Thread(target=worker, args=(i,), daemon=True) for i in range(workers)]
    for th in threads:
        th.start()
    for th in threads:
        th.join(timeout=seconds + 10)
    stop.set()
    elapsed = max(time.monotonic() - t0, 1e-9)
    out = {
        "url": url,
        "seconds": round(elapsed, 3),
        "workers": workers,
        "accepted": ok,
        "failed": fail,
        "accepted_per_sec": round(ok / elapsed, 2),
        "note": "local observation only; not a product throughput claim",
    }
    print(json.dumps(out, indent=2))
    if any("unauthorized" in str(fail) for _ in ()):
        pass
    return 0 if ok > 0 else 1


if __name__ == "__main__":
    raise SystemExit(main())
