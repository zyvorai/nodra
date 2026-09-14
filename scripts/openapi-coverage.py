#!/usr/bin/env python3
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
"""Fail if control-plane HTTP routes are missing from docs/openapi.yaml."""
from __future__ import annotations

import pathlib
import re
import sys

import yaml

ROOT = pathlib.Path(__file__).resolve().parents[1]
SERVER = ROOT / "internal" / "server" / "server.go"
OPENAPI = ROOT / "docs" / "openapi.yaml"

# Patterns registered on the mux (Go 1.22+ method routes).
ROUTE_RE = re.compile(
    r'''(?:HandleFunc|Handle)\(\s*"(?:GET|POST|PUT|PATCH|DELETE)\s+([^"]+)"'''
)


def normalize(path: str) -> str:
    path = path.strip()
    if path.startswith("/api/v1"):
        return path[len("/api/v1") :] or "/"
    return path


def main() -> int:
    src = SERVER.read_text()
    code_paths = {normalize(m.group(1)) for m in ROUTE_RE.finditer(src)}
    # Static UI assets are not part of the public API contract.
    code_paths -= {"/assets/{name}", "/"}

    oa = yaml.safe_load(OPENAPI.read_text())
    oa_paths = set(oa.get("paths", {}).keys())

    missing = sorted(p for p in code_paths if p not in oa_paths)
    if missing:
        print("OpenAPI missing routes:")
        for p in missing:
            print(f"  {p}")
        return 1
    print(f"openapi coverage ok ({len(code_paths)} routes)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
