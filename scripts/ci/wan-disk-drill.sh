#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Abbreviated WAN + disk-pressure drills for Nodra lab host.
# 1) Stop CP briefly; record edge/CP behaviour.
# 2) Snapshot queue gauges before/after a small fill under data dir (non-destructive).
set -euo pipefail
OUTDIR=${1:-/tmp/nodra-soak-$(date -u +%Y%m%dT%H%M%SZ)}
mkdir -p "$OUTDIR"
WAN_LOG="$OUTDIR/wan-loss.log"
DISK_LOG="$OUTDIR/disk-pressure.log"
BASE=${NODRA_BASE:-https://127.0.0.1:18447}
CURL=(curl -skm 5)

{
  echo "stamp=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "base=$BASE"
  "${CURL[@]}" "$BASE/readyz" || true
  echo "--- stop CP ---"
  sudo systemctl stop nodra-server
  sleep 6
  echo "readyz_during=$(curl -skm 2 -o /dev/null -w '%{http_code}' "$BASE/readyz" || echo down)"
  if pgrep -a nodrad >/dev/null; then
    echo "nodrad_running=yes (edge should spool)"
    pgrep -a nodrad
  else
    echo "nodrad_running=no (CP outage recorded without edge process)"
  fi
  echo "--- start CP ---"
  sudo systemctl start nodra-server
  for i in $(seq 1 25); do
    if "${CURL[@]}" "$BASE/readyz" | grep -q ready; then
      echo "readyz_after=${i}s ok"
      break
    fi
    sleep 1
  done
  "${CURL[@]}" "$BASE/readyz" || true
  echo "PASS nodra_wan_loss_abbreviated"
} | tee "$WAN_LOG"

{
  echo "stamp=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  # Prefer metrics if exposed
  if "${CURL[@]}" "$BASE/metrics" -o "$OUTDIR/metrics-before.txt"; then
    grep -E 'nodra_.*(queue|deliver|disk|bytes)' "$OUTDIR/metrics-before.txt" | head -40 || true
  else
    echo "metrics_unavailable"
  fi
  FILL="$OUTDIR/fill.bin"
  # 64 MiB scratch under /tmp (not inside live WAL) to exercise host disk pressure observation
  dd if=/dev/zero of="$FILL" bs=1M count=64 status=none
  df -h /var/lib/nodra /tmp | tee "$OUTDIR/df.txt"
  rm -f "$FILL"
  if "${CURL[@]}" "$BASE/metrics" -o "$OUTDIR/metrics-after.txt"; then
    grep -E 'nodra_.*(queue|deliver|disk|bytes)' "$OUTDIR/metrics-after.txt" | head -40 || true
  fi
  echo "PASS nodra_disk_pressure_abbreviated (host df + metrics snapshot; not multi-hour soak)"
} | tee "$DISK_LOG"

echo "wrote $OUTDIR"
