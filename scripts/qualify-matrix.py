#!/usr/bin/env python3
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
"""Software qualification matrix for Nodra."""
from __future__ import annotations

import json
import os
import pathlib
import re
import subprocess
import sys
from datetime import datetime, timezone

ROOT = pathlib.Path(__file__).resolve().parents[1]
EVIDENCE = ROOT / "evidence" / "qualification"
VERSION_FILE = (ROOT / "VERSION").read_text().strip()


def run(cmd, **kwargs):
    return subprocess.run(cmd, cwd=ROOT, text=True, capture_output=True, **kwargs)


def row(results, name, status, detail=""):
    results.append({"id": name, "status": status, "detail": detail, "class": "software"})
    mark = "PASS" if status == "pass" else ("SKIP" if status == "skip" else "FAIL")
    print(f"[{mark}] {name}" + (f" — {detail}" if detail else ""))


def assert_version_lockstep(results):
    expected = VERSION_FILE
    checks = {
        "Makefile": (ROOT / "Makefile").read_text(),
        "Dockerfile": (ROOT / "Dockerfile").read_text(),
        "Chart.yaml": (ROOT / "charts/nodra/Chart.yaml").read_text(),
        "values.yaml": (ROOT / "charts/nodra/values.yaml").read_text(),
        "openapi.yaml": (ROOT / "docs/openapi.yaml").read_text(),
        "version.go": (ROOT / "internal/version/version.go").read_text(),
    }
    failures = []
    if not re.search(rf'VERSION \?= {re.escape(expected)}\b', checks["Makefile"]):
        failures.append("Makefile VERSION")
    if not re.search(rf'ARG VERSION={re.escape(expected)}\b', checks["Dockerfile"]):
        failures.append("Dockerfile ARG VERSION")
    if f'appVersion: "{expected}"' not in checks["Chart.yaml"]:
        failures.append("Chart appVersion")
    if f'tag: "{expected}"' not in checks["values.yaml"]:
        failures.append("Helm image.tag")
    if f"version: {expected}" not in checks["openapi.yaml"]:
        failures.append("OpenAPI version")
    if f'Version   = "{expected}"' not in checks["version.go"]:
        failures.append("internal/version.Version")
    if failures:
        row(results, "version_lockstep", "fail", ", ".join(failures))
    else:
        row(results, "version_lockstep", "pass", expected)


def main():
    EVIDENCE.mkdir(parents=True, exist_ok=True)
    results = []
    started = datetime.now(timezone.utc).isoformat()

    assert_version_lockstep(results)

    proc = run(["sh", "-c", 'test -z "$(gofmt -l .)"'])
    row(results, "gofmt", "pass" if proc.returncode == 0 else "fail", (proc.stdout + proc.stderr)[-200:])

    proc = run(["go", "vet", "./..."], timeout=120)
    row(results, "go_vet", "pass" if proc.returncode == 0 else "fail", (proc.stdout + proc.stderr)[-300:])

    fast = os.environ.get("NODRA_QUALIFY_FAST", "") in ("1", "true", "yes")
    if fast:
        row(results, "unit_race", "skip", "NODRA_QUALIFY_FAST=1 — covered by release-check / CI race job")
    else:
        proc = run(["go", "test", "-race", "./..."], timeout=600)
        row(results, "unit_race", "pass" if proc.returncode == 0 else "fail", (proc.stdout + proc.stderr)[-400:])

    proc = run(["go", "test", "./internal/server/", "-count=1", "-run", "Readyz"], timeout=60)
    row(results, "readyz_store_ping", "pass" if proc.returncode == 0 else "fail", (proc.stdout + proc.stderr)[-300:])

    proc = run(["python3", "-c", "import yaml, pathlib; list(yaml.safe_load_all(pathlib.Path('docs/openapi.yaml').read_text()))"])
    row(results, "openapi_yaml_parse", "pass" if proc.returncode == 0 else "fail", (proc.stdout + proc.stderr)[-200:])

    proc = run(["python3", "scripts/openapi-coverage.py"], timeout=30)
    row(results, "openapi_route_coverage", "pass" if proc.returncode == 0 else "fail", (proc.stdout + proc.stderr)[-300:])

    proc = run(["make", "build"], timeout=180)
    row(results, "build_binaries", "pass" if proc.returncode == 0 else "fail", (proc.stdout + proc.stderr)[-300:])

    if fast:
        row(results, "local_smoke", "skip", "NODRA_QUALIFY_FAST=1 — covered by release-check smoke")
    else:
        proc = run(["./scripts/smoke.sh"], timeout=300)
        row(results, "local_smoke", "pass" if proc.returncode == 0 else "fail", (proc.stdout + proc.stderr)[-400:])

    if os.environ.get("NODRA_DATABASE_URL"):
        proc = run(["go", "test", "./internal/store/", "-count=1", "-run", "Postgres"], timeout=120)
        row(results, "postgres_store_ci", "pass" if proc.returncode == 0 else "fail", (proc.stdout + proc.stderr)[-400:])
    else:
        row(results, "postgres_store_ci", "skip", "set NODRA_DATABASE_URL — covered by CI postgres job")

    for name, detail in [
        ("backup_restore_drill", "operator-signed — evidence/qualification/ops-checklist.md"),
        ("wan_loss_disk_pressure_soak", "operator-signed multi-hour soak; see docs/QUALIFICATION.md"),
        ("ha_multi_writer", "not claimed in v0.2.x — ROADMAP v1.0"),
    ]:
        row(results, name, "skip", detail)

    report = {
        "generated_at": started,
        "finished_at": datetime.now(timezone.utc).isoformat(),
        "product": "nodra",
        "version": VERSION_FILE,
        "host": os.uname().sysname if hasattr(os, "uname") else "unknown",
        "results": results,
        "software_pass": all(r["status"] == "pass" for r in results if r["status"] != "skip"),
        "ops_claimed": False,
        "note": "Ops/lab rows are skip until evidence/qualification/ops-checklist.md is signed.",
    }
    out = EVIDENCE / "software-matrix.json"
    out.write_text(json.dumps(report, indent=2) + "\n")
    print(f"\nwrote {out}")
    if not report["software_pass"]:
        sys.exit(1)
    print("software qualification rows passed; ops checklist still required for production")


if __name__ == "__main__":
    main()
