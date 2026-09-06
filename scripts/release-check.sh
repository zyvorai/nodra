#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
echo '[1/7] formatting'
FILES="$(gofmt -l .)"; test -z "$FILES" || { echo "$FILES"; exit 1; }
echo '[2/7] vet'
go vet ./...
echo '[3/7] race tests'
go test -race ./...
echo '[4/7] builds'
make build >/dev/null
echo '[5/7] YAML parse (non-template manifests/workflows)'
python3 - <<'PY'
import pathlib, yaml
files=list(pathlib.Path('deployments/kubernetes').glob('*.yaml'))+list(pathlib.Path('.github/workflows').glob('*.yml'))+[pathlib.Path('docs/openapi.yaml')]
for p in files:
    list(yaml.safe_load_all(p.read_text()))
print(f'parsed {len(files)} YAML files')
PY
echo '[6/7] local end-to-end smoke'
./scripts/smoke.sh
echo '[7/7] license/readme checks'
test -s LICENSE; test -s README.md; grep -q 'Apache License' LICENSE; grep -q 'Offline-first' README.md
echo 'release-check: PASS'
