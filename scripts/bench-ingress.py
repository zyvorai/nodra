#!/usr/bin/env python3
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
"""Measure a short local HTTP publish envelope against a running edge agent.

This prints observed accepts/sec for the configured duration. It is not a
certified rating and must not be quoted as one without the lab host, duration,
and commit recorded in the output.

Usage:
  NODRA_BENCH_URL=http://127.0.0.1:9091 \\
  NODRA_BENCH_TOKEN=nodra-demo-local \\
  NODRA_BENCH_SECONDS=30 \\
  python3 scripts/bench-ingress.py
"""

from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.request


def main() -> int:
    url = os.environ.get("NODRA_BENCH_URL", "http://127.0.0.1:9091").rstrip("/") + "/v1/publish"
    token = os.environ.get("NODRA_BENCH_TOKEN", "nodra-demo-local")
    seconds = float(os.environ.get("NODRA_BENCH_SECONDS", "30"))
    topic = os.environ.get("NODRA_BENCH_TOPIC", "bench/ingress")
    deadline = time.monotonic() + seconds
    ok = fail = 0
    t0 = time.monotonic()
    while time.monotonic() < deadline:
        body = json.dumps({"topic": topic, "payload": {"n": ok + fail, "t": time.time()}}).encode()
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
                if 200 <= resp.status < 300:
                    ok += 1
                else:
                    fail += 1
        except urllib.error.HTTPError as e:
            fail += 1
            if e.code == 401:
                print("unauthorized: set NODRA_BENCH_TOKEN", file=sys.stderr)
                return 2
        except Exception:
            fail += 1
    elapsed = max(time.monotonic() - t0, 1e-9)
    out = {
        "url": url,
        "seconds": round(elapsed, 3),
        "accepted": ok,
        "failed": fail,
        "accepted_per_sec": round(ok / elapsed, 2),
        "note": "local observation only; not a product throughput claim",
    }
    print(json.dumps(out, indent=2))
    return 0 if ok > 0 else 1


if __name__ == "__main__":
    raise SystemExit(main())
