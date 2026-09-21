#!/usr/bin/env bash
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
#
# Duration-configurable WAN-loss + disk-pressure soak driver for Nodra.
#
# Replaces the old scripts/ci/wan-disk-drill.sh (single stop/restart + one
# 64MiB fill) with: repeated WAN-loss cycles, sustained/progressive disk
# pressure against the real control-plane data volume, and continuous
# metrics/health sampling for the whole run. Duration and cycle cadence are
# all configurable so the same script can run as a short PR-gated smoke test
# and as a long scheduled soak.
#
# Modes:
#   default        drives the repo's docker-compose.yml stack (portable to
#                  GitHub-hosted runners).
#   NODRA_SOAK_LAB=1  drives a bare-metal `nodra-server`/`nodrad` install via
#                  systemctl, mirroring the retired lab drill procedure.
#
# Env vars (all optional):
#   NODRA_SOAK_DURATION        total run time, e.g. 10m, 4h, 72h (default 10m)
#   NODRA_SOAK_WAN_CYCLE       time between WAN-loss cycles (default 90s)
#   NODRA_SOAK_WAN_DOWNTIME    how long control-plane stays down per cycle (default 10s)
#   NODRA_SOAK_DISK_CYCLE      time between disk-pressure cycles (default 60s)
#   NODRA_SOAK_DISK_TARGET_MB  peak fill size per disk-pressure ramp (default 256)
#   NODRA_SOAK_SAMPLE_INTERVAL background sampler cadence (default 5s)
#   NODRA_SOAK_READY_CEILING   max acceptable time-to-ready in seconds (default 30)
#   NODRA_SOAK_INGEST_INTERVAL seconds between HTTP and MQTT publishes (default 2s)
#   NODRA_SOAK_LAB             1 = bare-metal/systemctl mode instead of compose
#   NODRA_SOAK_CP_BASE         control-plane base URL (default http://127.0.0.1:8080)
#   NODRA_SOAK_AGENT_BASE      edge-agent base URL (default http://127.0.0.1:9091)
#   NODRA_MQTT_PORT            host port of the edge MQTT listener (default 1883)
#   NODRA_LOCAL_TOKEN          edge local bearer (default nodra-demo-local)
#
# Output: $OUTDIR/{samples.jsonl,wan-cycles.jsonl,disk-cycles.jsonl,summary.json}
# Pass/fail is judged separately by scripts/ci/soak-check.py against summary.json
# — this script's own exit code only reflects "did the drill run to completion."
set -euo pipefail

# --- duration parsing (no GNU-date dependency; portable to macOS/BSD too) ---
to_seconds() {
  local input="$1" total=0
  if [[ "$input" =~ ^[0-9]+$ ]]; then
    echo "$input"
    return
  fi
  while [[ "$input" =~ ([0-9]+)([smhd]) ]]; do
    local num="${BASH_REMATCH[1]}" unit="${BASH_REMATCH[2]}"
    case "$unit" in
      s) total=$((total + num)) ;;
      m) total=$((total + num * 60)) ;;
      h) total=$((total + num * 3600)) ;;
      d) total=$((total + num * 86400)) ;;
    esac
    input="${input/${BASH_REMATCH[0]}/}"
  done
  echo "$total"
}

OUTDIR=${1:-/tmp/nodra-soak-$(date -u +%Y%m%dT%H%M%SZ)}
mkdir -p "$OUTDIR"

DURATION=$(to_seconds "${NODRA_SOAK_DURATION:-10m}")
WAN_CYCLE=$(to_seconds "${NODRA_SOAK_WAN_CYCLE:-90s}")
WAN_DOWNTIME=$(to_seconds "${NODRA_SOAK_WAN_DOWNTIME:-10s}")
DISK_CYCLE=$(to_seconds "${NODRA_SOAK_DISK_CYCLE:-60s}")
DISK_TARGET_MB=${NODRA_SOAK_DISK_TARGET_MB:-256}
SAMPLE_INTERVAL=$(to_seconds "${NODRA_SOAK_SAMPLE_INTERVAL:-5s}")
READY_CEILING=${NODRA_SOAK_READY_CEILING:-30}
INGEST_INTERVAL=$(to_seconds "${NODRA_SOAK_INGEST_INTERVAL:-2s}")
LAB_MODE=${NODRA_SOAK_LAB:-0}
MQTT_PORT=${NODRA_MQTT_PORT:-1883}
LOCAL_TOKEN=${NODRA_LOCAL_TOKEN:-nodra-demo-local}
ADMIN_TOKEN=${NODRA_ADMIN_TOKEN:-nodra-demo-admin}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
MQTT_PUBLISH="$ROOT/scripts/ci/mqtt_publish.py"

CP_BASE=${NODRA_SOAK_CP_BASE:-http://127.0.0.1:8080}
AGENT_BASE=${NODRA_SOAK_AGENT_BASE:-http://127.0.0.1:9091}
# HTTPS lab control planes use a self-signed cert; allow NODRA_SOAK_TLS_INSECURE=1
# or auto-enable when CP_BASE is https://.
CURL=(curl -sSm 5)
if [ "${NODRA_SOAK_TLS_INSECURE:-}" = "1" ] || [[ "$CP_BASE" == https://* ]]; then
  CURL=(curl -sSm 5 -k)
fi
SINK_URL=${NODRA_SOAK_SINK_URL:-http://127.0.0.1:18081/hook}
SINK_PID=""

SAMPLES="$OUTDIR/samples.jsonl"
WAN_LOG="$OUTDIR/wan-cycles.jsonl"
DISK_LOG="$OUTDIR/disk-cycles.jsonl"
SUMMARY="$OUTDIR/summary.json"
: >"$SAMPLES"
: >"$WAN_LOG"
: >"$DISK_LOG"

log() { echo "[soak] $*" >&2; }

compose() {
  docker compose -f "$ROOT/docker-compose.yml" -f "$ROOT/docker-compose.soak.yml" "$@"
}

# --- helpers over the running stack ---

cp_metric() {
  # cp_metric <metric_name> — extract the last integer value for a Prometheus
  # counter/gauge line from control-plane /metrics, defaulting to 0.
  local name="$1"
  "${CURL[@]}" "$CP_BASE/metrics" 2>/dev/null | grep -E "^${name} " | tail -1 | awk '{print $2}' | grep -E '^[0-9]+$' || echo 0
}

cp_ready() {
  "${CURL[@]}" -o /dev/null -w '%{http_code}' "$CP_BASE/readyz" 2>/dev/null || echo 000
}

agent_healthz_json() {
  "${CURL[@]}" "$AGENT_BASE/healthz" 2>/dev/null || echo '{}'
}

container_id() {
  local service="$1"
  compose ps -q "$service" 2>/dev/null || true
}

restart_count() {
  local service="$1" cid
  cid=$(container_id "$service")
  [ -n "$cid" ] || { echo 0; return; }
  docker inspect -f '{{.RestartCount}}' "$cid" 2>/dev/null || echo 0
}

container_stats_json() {
  # {"mem_bytes": N, "cpu_pct": N} for one compose service, best-effort.
  local service="$1" cid
  cid=$(container_id "$service")
  if [ -z "$cid" ]; then
    echo '{"mem_bytes":0,"cpu_pct":0}'
    return
  fi
  docker stats --no-stream --format '{{json .}}' "$cid" 2>/dev/null \
    | python3 -c '
import json,sys
try:
    d = json.load(sys.stdin)
    mem = d.get("MemUsage","0B / 0B").split("/")[0].strip()
    def to_bytes(s):
        units = {"B":1,"KiB":1024,"MiB":1024**2,"GiB":1024**3}
        for u in sorted(units, key=len, reverse=True):
            if s.endswith(u):
                return int(float(s[:-len(u)]) * units[u])
        return 0
    cpu = float(d.get("CPUPerc","0%").rstrip("%") or 0)
    print(json.dumps({"mem_bytes": to_bytes(mem), "cpu_pct": cpu}))
except Exception:
    print(json.dumps({"mem_bytes":0,"cpu_pct":0}))
' 2>/dev/null || echo '{"mem_bytes":0,"cpu_pct":0}'
}

disk_usage_pct() {
  local path="$1"
  df -P "$path" 2>/dev/null | awk 'NR==2 {gsub("%","",$5); print $5}' || echo 0
}

data_volume_path() {
  if [ "$LAB_MODE" = "1" ]; then
    echo "/var/lib/nodra"
  else
    # Host-visible mountpoint for the control-data volume; falls back to /
    # (still useful as a directional signal) if Docker won't report it.
    docker volume inspect --format '{{.Mountpoint}}' "$(basename "$(pwd)")_control-data" 2>/dev/null \
      || docker volume inspect --format '{{.Mountpoint}}' nodra_control-data 2>/dev/null \
      || echo "/"
  fi
}

cp_stop() {
  if [ "$LAB_MODE" = "1" ]; then sudo systemctl stop nodra-server
  else compose stop control-plane >/dev/null
  fi
}

cp_start() {
  if [ "$LAB_MODE" = "1" ]; then sudo systemctl start nodra-server
  else compose start control-plane >/dev/null
  fi
}

fill_disk() {
  local mb="$1"
  if [ "$LAB_MODE" = "1" ]; then
    sudo dd if=/dev/zero of=/var/lib/nodra/.soak-fill.bin bs=1M count="$mb" status=none 2>/dev/null || true
  else
    compose exec -T control-plane sh -c "dd if=/dev/zero of=/var/lib/nodra/.soak-fill.bin bs=1M count=$mb status=none" >/dev/null 2>&1 || true
  fi
}

release_disk() {
  if [ "$LAB_MODE" = "1" ]; then
    sudo rm -f /var/lib/nodra/.soak-fill.bin
  else
    compose exec -T control-plane sh -c 'rm -f /var/lib/nodra/.soak-fill.bin' >/dev/null 2>&1 || true
  fi
}

wait_ready() {
  # Polls readyz once per second up to $1 seconds; echoes elapsed seconds (or -1 on timeout).
  local ceiling="$1" i
  for ((i = 1; i <= ceiling; i++)); do
    if [ "$(cp_ready)" = "200" ]; then
      echo "$i"
      return 0
    fi
    sleep 1
  done
  echo -1
}

# --- bring up the stack (compose mode only) ---

STACK_STARTED_HERE=0
if [ "$LAB_MODE" != "1" ]; then
  log "starting compose stack (control-plane, edge-agent, sink; simulator stays stopped)"
  export NODRA_ADMIN_TOKEN=${NODRA_ADMIN_TOKEN:-nodra-demo-admin}
  export NODRA_ADMIN_PASSWORD=${NODRA_ADMIN_PASSWORD:-nodra-demo-admin}
  export NODRA_ENROLLMENT_TOKEN=${NODRA_ENROLLMENT_TOKEN:-nodra-demo-enroll}
  export NODRA_LOCAL_TOKEN="$LOCAL_TOKEN"
  compose up --build -d control-plane edge-agent sink
  STACK_STARTED_HERE=1
  for i in $(seq 1 60); do
    [ "$(cp_ready)" = "200" ] && break
    sleep 2
  done
fi

RESTARTS_CP_START=$( [ "$LAB_MODE" != "1" ] && restart_count control-plane || echo 0)
RESTARTS_AGENT_START=$( [ "$LAB_MODE" != "1" ] && restart_count edge-agent || echo 0)

EVENTS_START=$(cp_metric nodra_events_total)
DELIVERIES_START=$(cp_metric nodra_deliveries_total)
DEADLETTERS_START=$(cp_metric nodra_dead_letters)
FAILURES_START=$(cp_metric nodra_delivery_failures_total)

# --- cleanup / always write partial evidence ---

CYCLES_RUN=0
DISK_CYCLES_RUN=0

write_summary() {
  local interrupted="$1"
  local events_end deliveries_end deadletters_end failures_end pending_end
  events_end=$(cp_metric nodra_events_total)
  deliveries_end=$(cp_metric nodra_deliveries_total)
  deadletters_end=$(cp_metric nodra_dead_letters)
  failures_end=$(cp_metric nodra_delivery_failures_total)
  pending_end=$(cp_metric nodra_pending_deliveries)
  local restarts_cp_end restarts_agent_end
  restarts_cp_end=$( [ "$LAB_MODE" != "1" ] && restart_count control-plane || echo 0)
  restarts_agent_end=$( [ "$LAB_MODE" != "1" ] && restart_count edge-agent || echo 0)

  python3 - "$SUMMARY" "$SAMPLES" "$OUTDIR/ingest.ok" <<PYEOF
import json, sys
from pathlib import Path

def monotonic(vals):
    # Counters are process-local and drop to 0 when the control plane restarts.
    # Seed from 0 so the first sample is counted, and add the new value after a reset.
    gained = 0
    prev = 0
    for v in vals:
        if v >= prev:
            gained += v - prev
        else:
            gained += v
        prev = v
    return gained

def series(samples, key):
    return [int(s.get(key) or 0) for s in samples]

samples = []
sample_path = Path(sys.argv[2])
if sample_path.exists():
    for line in sample_path.read_text().splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            samples.append(json.loads(line))
        except json.JSONDecodeError:
            continue
ingest = Path(sys.argv[3])
http_accepted = mqtt_published = 0
if ingest.exists():
    for line in ingest.read_text().splitlines():
        if line.strip() == "http":
            http_accepted += 1
        elif line.strip() == "mqtt":
            mqtt_published += 1
spool_end = 0
if samples:
    health = samples[-1].get("agent_healthz") or {}
    spool_end = int(((health.get("spool") or {}).get("items")) or 0)
out = {
    "generated_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
    "duration_s": $DURATION,
    "interrupted": bool(int("$interrupted")),
    "lab_mode": $( [ "$LAB_MODE" = "1" ] && echo True || echo False ),
    "wan_cycles_run": $CYCLES_RUN,
    "disk_cycles_run": $DISK_CYCLES_RUN,
    "ready_ceiling_s": $READY_CEILING,
    "samples_path": "samples.jsonl",
    "wan_cycles_path": "wan-cycles.jsonl",
    "disk_cycles_path": "disk-cycles.jsonl",
    "metrics": {
        "events_start": $EVENTS_START, "events_end": $events_end,
        "deliveries_start": $DELIVERIES_START, "deliveries_end": $deliveries_end,
        "dead_letters_start": $DEADLETTERS_START, "dead_letters_end": $deadletters_end,
        "delivery_failures_start": $FAILURES_START, "delivery_failures_end": $failures_end,
        "pending_deliveries_end": $pending_end,
    },
    "integrity": {
        "http_accepted": http_accepted,
        "mqtt_published": mqtt_published,
        "events_persisted": monotonic(series(samples, "events")),
        "deliveries_forwarded": monotonic(series(samples, "deliveries")),
        "dead_letters": monotonic(series(samples, "dead_letters")),
        "duplicates": monotonic(series(samples, "duplicates")),
        "pending_end": $pending_end,
        "spool_end": spool_end,
    },
    "restarts": {
        "control_plane_start": $RESTARTS_CP_START, "control_plane_end": $restarts_cp_end,
        "edge_agent_start": $RESTARTS_AGENT_START, "edge_agent_end": $restarts_agent_end,
    },
}
with open(sys.argv[1], "w") as f:
    json.dump(out, f, indent=2)
    f.write("\n")
PYEOF
  log "wrote $SUMMARY"
}

BG_PIDS=()
cleanup() {
  local ec=$?
  trap - EXIT INT TERM
  log "cleanup: stopping background loops (exit code so far: $ec)"
  for pid in "${BG_PIDS[@]:-}"; do
    kill "$pid" >/dev/null 2>&1 || true
  done
  wait 2>/dev/null || true
  release_disk || true
  capture_heap heap-end || true
  write_summary "$( [ "$ec" != "0" ] && echo 1 || echo 0 )"
  if [ "$STACK_STARTED_HERE" = "1" ]; then
    compose down -v --remove-orphans >/dev/null 2>&1 || true
  fi
  exit "$ec"
}
trap cleanup EXIT INT TERM

capture_heap() {
  local name="$1"
  if [ "$LAB_MODE" = "1" ]; then
    return 0
  fi
  if curl -fsS "http://127.0.0.1:6060/debug/pprof/heap" -o "$OUTDIR/${name}.pprof"; then
    log "wrote $OUTDIR/${name}.pprof"
  else
    log "heap profile $name unavailable"
    rm -f "$OUTDIR/${name}.pprof"
  fi
}

prepare_sink_route() {
  local i code=000
  local target="$SINK_URL"
  if [ "$LAB_MODE" != "1" ]; then
    target="http://sink:8080/hook"
  fi
  for i in $(seq 1 30); do
    code=$("${CURL[@]}" -o /dev/null -w '%{http_code}' \
      -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
      -d "{\"name\":\"soak-sink\",\"topic\":\"soak/ingest\",\"target_url\":\"$target\",\"method\":\"POST\",\"enabled\":true,\"retry_max\":3,\"timeout_seconds\":5}" \
      "$CP_BASE/api/v1/routes" || true)
    if [ "$code" = "201" ] || [ "$code" = "200" ]; then
      log "soak ingest route created → $target (HTTP $code)"
      return 0
    fi
    sleep 2
  done
  log "soak ingest route was not created (last HTTP $code)"
}

start_lab_sink() {
  local port
  port=$(echo "$SINK_URL" | sed -n 's|.*://[^:]*:\([0-9]*\)/.*|\1|p')
  port=${port:-18081}
  python3 - "$port" <<'PY' &
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
port = int(sys.argv[1])
class H(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        if n:
            self.rfile.read(n)
        self.send_response(204)
        self.end_headers()
    def log_message(self, fmt, *args):
        return
ThreadingHTTPServer(("127.0.0.1", port), H).serve_forever()
PY
  SINK_PID=$!
  BG_PIDS+=("$SINK_PID")
  log "lab sink listening on 127.0.0.1:$port (pid $SINK_PID)"
}

ingest_loop() {
  local end=$((SECONDS + DURATION)) n=0 code
  : >"$OUTDIR/ingest.ok"
  while [ "$SECONDS" -lt "$end" ]; do
    n=$((n + 1))
    code=$("${CURL[@]}" -o /dev/null -w '%{http_code}' \
      -H "Authorization: Bearer $LOCAL_TOKEN" -H 'Content-Type: application/json' \
      -d "{\"topic\":\"soak/ingest\",\"payload\":{\"n\":$n,\"via\":\"http\"}}" \
      "$AGENT_BASE/v1/publish" || true)
    if [ "$code" = "202" ]; then
      echo http >>"$OUTDIR/ingest.ok"
    fi
    if python3 "$MQTT_PUBLISH" 127.0.0.1 "$MQTT_PORT" soak/ingest "{\"n\":$n,\"via\":\"mqtt\"}"; then
      echo mqtt >>"$OUTDIR/ingest.ok"
    fi
    sleep "$INGEST_INTERVAL"
  done
}

heap_loop() {
  capture_heap heap-start
  sleep $((DURATION / 2))
  [ "$SECONDS" -lt "$DURATION" ] || return 0
  capture_heap heap-mid
}

# --- background sampler: runs for the whole soak, independent of the two drill loops ---

sampler_loop() {
  local end=$((SECONDS + DURATION))
  local vol
  vol=$(data_volume_path)
  while [ "$SECONDS" -lt "$end" ]; do
    local ts ready spool cp_stats agent_stats disk_pct events deliveries dead dupes pending
    ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    ready=$(cp_ready)
    spool=$(agent_healthz_json)
    disk_pct=$(disk_usage_pct "$vol")
    events=$(cp_metric nodra_events_total)
    deliveries=$(cp_metric nodra_deliveries_total)
    dead=$(cp_metric nodra_dead_letters)
    dupes=$(cp_metric nodra_events_duplicate_total)
    pending=$(cp_metric nodra_pending_deliveries)
    if [ "$LAB_MODE" != "1" ]; then
      cp_stats=$(container_stats_json control-plane)
      agent_stats=$(container_stats_json edge-agent)
    else
      cp_stats='{"mem_bytes":0,"cpu_pct":0}'
      agent_stats='{"mem_bytes":0,"cpu_pct":0}'
    fi
    printf '{"ts":"%s","cp_ready_code":"%s","disk_pct":%s,"agent_healthz":%s,"cp_stats":%s,"agent_stats":%s,"events":%s,"deliveries":%s,"dead_letters":%s,"duplicates":%s,"pending":%s}\n' \
      "$ts" "$ready" "${disk_pct:-0}" "$spool" "$cp_stats" "$agent_stats" \
      "${events:-0}" "${deliveries:-0}" "${dead:-0}" "${dupes:-0}" "${pending:-0}" >>"$SAMPLES"
    sleep "$SAMPLE_INTERVAL"
  done
}

# --- WAN-loss loop: repeated stop/restart cycles for the whole soak duration ---

wan_loop() {
  local end=$((SECONDS + DURATION))
  while [ "$SECONDS" -lt "$end" ]; do
    sleep "$WAN_CYCLE"
    [ "$SECONDS" -ge "$end" ] && break
    local pre_spool post_spool t0 outage_start outage_end time_to_ready
    pre_spool=$(agent_healthz_json)
    outage_start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    cp_stop
    sleep "$WAN_DOWNTIME"
    t0=$SECONDS
    cp_start
    time_to_ready=$(wait_ready "$READY_CEILING")
    outage_end=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    post_spool=$(agent_healthz_json)
    CYCLES_RUN=$((CYCLES_RUN + 1))
    printf '{"cycle":%d,"outage_start":"%s","outage_end":"%s","time_to_ready_s":%s,"pre_outage_agent_healthz":%s,"post_restore_agent_healthz":%s}\n' \
      "$CYCLES_RUN" "$outage_start" "$outage_end" "$time_to_ready" "$pre_spool" "$post_spool" >>"$WAN_LOG"
    log "wan cycle $CYCLES_RUN done (time_to_ready=${time_to_ready}s)"
  done
}

# --- disk-pressure loop: progressive ramp/hold/release against the real data volume ---

disk_loop() {
  local end=$((SECONDS + DURATION))
  local steps=(25 50 75 100)
  while [ "$SECONDS" -lt "$end" ]; do
    sleep "$DISK_CYCLE"
    [ "$SECONDS" -ge "$end" ] && break
    local before after vol
    vol=$(data_volume_path)
    before=$(disk_usage_pct "$vol")
    for pct in "${steps[@]}"; do
      local mb=$((DISK_TARGET_MB * pct / 100))
      fill_disk "$mb"
      sleep 2
    done
    after=$(disk_usage_pct "$vol")
    release_disk
    sleep 2
    local released
    released=$(disk_usage_pct "$vol")
    DISK_CYCLES_RUN=$((DISK_CYCLES_RUN + 1))
    printf '{"cycle":%d,"disk_pct_before":%s,"disk_pct_peak":%s,"disk_pct_after_release":%s,"target_mb":%d}\n' \
      "$DISK_CYCLES_RUN" "${before:-0}" "${after:-0}" "${released:-0}" "$DISK_TARGET_MB" >>"$DISK_LOG"
    log "disk cycle $DISK_CYCLES_RUN done (before=${before}% peak=${after}% after_release=${released}%)"
  done
}

if [ "$LAB_MODE" = "1" ]; then
  start_lab_sink
  prepare_sink_route
elif [ "$LAB_MODE" != "1" ]; then
  prepare_sink_route
fi
SECONDS=0
log "soak starting: duration=${DURATION}s wan_cycle=${WAN_CYCLE}s wan_downtime=${WAN_DOWNTIME}s disk_cycle=${DISK_CYCLE}s ingest_interval=${INGEST_INTERVAL}s sample_interval=${SAMPLE_INTERVAL}s lab_mode=${LAB_MODE}"

sampler_loop &
BG_PIDS+=("$!")
wan_loop &
BG_PIDS+=("$!")
disk_loop &
BG_PIDS+=("$!")
ingest_loop &
BG_PIDS+=("$!")
if [ "$LAB_MODE" != "1" ]; then
  heap_loop &
  BG_PIDS+=("$!")
fi

# Controller: block until the soak duration elapses, then drain and fall through to cleanup.
while [ "$SECONDS" -lt "$DURATION" ]; do
  sleep 1
done

log "soak duration elapsed, draining edge spool"
sleep "${NODRA_SOAK_DRAIN:-15}"
log "tearing down"
# cleanup() runs via the EXIT trap.
