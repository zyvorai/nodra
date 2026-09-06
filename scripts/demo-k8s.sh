#!/usr/bin/env bash
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
# demo-k8s.sh — Deploy Nodra to Kubernetes (kind by default) with live simulation
# ============================================================================
# Customer-demo ready:
#   control plane + edge agent + nodra-sim (3 sites, devices, routes, heartbeats)
#
# Usage:
#   ./scripts/demo-k8s.sh              # create/use kind cluster, install, port-forward
#   ./scripts/demo-k8s.sh --no-kind    # use current kube context
#   ./scripts/demo-k8s.sh --port 18080
#   ./scripts/demo-k8s.sh --uninstall
#
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CLUSTER="${NODRA_KIND_CLUSTER:-nodra-demo}"
NS="${NODRA_DEMO_NS:-nodra-demo}"
RELEASE="${NODRA_HELM_RELEASE:-nodra}"
IMAGE_REPO="${NODRA_IMAGE_REPO:-nodra}"
IMAGE_TAG="${NODRA_IMAGE_TAG:-demo}"
LOCAL_PORT="${NODRA_DEMO_PORT:-18080}"
USE_KIND=1
UNINSTALL=0

while [ $# -gt 0 ]; do
  case "$1" in
    --no-kind) USE_KIND=0; shift ;;
    --uninstall) UNINSTALL=1; shift ;;
    --port) LOCAL_PORT="$2"; shift 2 ;;
    --port=*) LOCAL_PORT="${1#*=}"; shift ;;
    --cluster) CLUSTER="$2"; shift 2 ;;
    --namespace) NS="$2"; shift 2 ;;
    --help|-h) sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done

need() { command -v "$1" >/dev/null || { echo "missing required tool: $1" >&2; exit 1; }; }
need docker
need kubectl
need helm

if [ "$UNINSTALL" = 1 ]; then
  helm uninstall "$RELEASE" -n "$NS" 2>/dev/null || true
  kubectl delete ns "$NS" --ignore-not-found
  if [ "$USE_KIND" = 1 ] && command -v kind >/dev/null && kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
    kind delete cluster --name "$CLUSTER"
  fi
  echo "  ✨ nodra demo uninstalled"
  exit 0
fi

if [ "$USE_KIND" = 1 ]; then
  need kind
  if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
    echo "  🔧 Creating kind cluster ${CLUSTER}"
    kind create cluster --name "$CLUSTER"
  else
    echo "  ✅ Using existing kind cluster ${CLUSTER}"
  fi
  kubectl config use-context "kind-${CLUSTER}" >/dev/null
fi

echo "  🔧 Building image ${IMAGE_REPO}:${IMAGE_TAG}"
docker build -t "${IMAGE_REPO}:${IMAGE_TAG}" "$ROOT"

if [ "$USE_KIND" = 1 ]; then
  echo "  📦 Loading image into kind"
  kind load docker-image "${IMAGE_REPO}:${IMAGE_TAG}" --name "$CLUSTER"
fi

echo "  🚀 Helm install ${RELEASE} in ${NS}"
kubectl get ns "$NS" >/dev/null 2>&1 || kubectl create namespace "$NS"
kubectl label namespace "$NS" \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/enforce-version=latest \
  --overwrite >/dev/null

helm upgrade --install "$RELEASE" "$ROOT/charts/nodra" \
  --namespace "$NS" \
  -f "$ROOT/charts/nodra/values-demo.yaml" \
  --set "image.repository=${IMAGE_REPO}" \
  --set "image.tag=${IMAGE_TAG}" \
  --set "image.pullPolicy=Never" \
  --set "agent.serverURL=http://${RELEASE}:8080" \
  --set "simulation.serverURL=http://${RELEASE}:8080" \
  --wait --timeout 180s

echo "  ⏳ Waiting for control plane + simulation"
kubectl -n "$NS" rollout status "deploy/${RELEASE}" --timeout=120s
kubectl -n "$NS" rollout status "deploy/${RELEASE}-sim" --timeout=120s
kubectl -n "$NS" rollout status "deploy/${RELEASE}-agent" --timeout=120s || true

# Give sim a moment to seed
sleep 3
kubectl -n "$NS" logs "deploy/${RELEASE}-sim" --tail=20 || true

PF_LOG="$(mktemp)"
kubectl -n "$NS" port-forward "svc/${RELEASE}" "${LOCAL_PORT}:8080" >"$PF_LOG" 2>&1 &
PF_PID=$!
cleanup_pf() { kill "$PF_PID" 2>/dev/null || true; }
trap cleanup_pf EXIT

for _ in $(seq 1 60); do
  curl -fsS "http://127.0.0.1:${LOCAL_PORT}/readyz" >/dev/null 2>&1 && break
  sleep 0.25
done
curl -fsS "http://127.0.0.1:${LOCAL_PORT}/readyz" >/dev/null

ADMIN_TOKEN="$(kubectl -n "$NS" get secret "${RELEASE}-secrets" -o jsonpath='{.data.admin-token}' | base64 -d)"
OV="$(curl -fsS -H "Authorization: Bearer ${ADMIN_TOKEN}" "http://127.0.0.1:${LOCAL_PORT}/api/v1/overview")"
echo "$OV" | grep -Eq '"sites":[1-9]' || { echo "overview missing sites: $OV" >&2; exit 1; }
echo "$OV" | grep -Eq '"online_sites":[1-9]' || { echo "overview missing online sites: $OV" >&2; exit 1; }

echo ""
echo "  ✨ Nodra customer demo is ready"
echo "     ╭──────────────────────────────────────────────────╮"
echo "     │ Dashboard  http://127.0.0.1:${LOCAL_PORT}/"
echo "     │ Sign in    admin / nodra-demo-admin"
echo "     │ Namespace  ${NS}"
echo "     │ Pods       control-plane + agent + simulation"
echo "     ╰──────────────────────────────────────────────────╯"
echo ""
echo "  Port-forward is running (pid ${PF_PID}). Ctrl+C to stop."
echo "  Keep this terminal open, or re-run:"
echo "    kubectl -n ${NS} port-forward svc/${RELEASE} ${LOCAL_PORT}:8080"
echo ""

# Keep port-forward alive for interactive demos
wait "$PF_PID" 2>/dev/null || true
