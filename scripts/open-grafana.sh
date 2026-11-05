#!/usr/bin/env bash
# open-grafana.sh -- Git Bash / macOS / Linux counterpart of open-grafana.ps1
# Usage: bash scripts/open-grafana.sh
# Ctrl+C stops the port-forwards started by this script.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

echo "==> check cluster context"
kubectl config use-context docker-desktop >/dev/null 2>&1 || true
kubectl get nodes --request-timeout=10s >/dev/null

echo "==> read grafana admin password"
B64="$(kubectl -n monitoring get secret monitoring-grafana -o jsonpath='{.data.admin-password}')"
if [ -z "$B64" ]; then
  echo "secret monitoring-grafana not found; is the monitoring release installed?" >&2
  exit 1
fi
PWD_GRAFANA="$(printf '%s' "$B64" | base64 -d)"

echo "==> start port-forwards"
kubectl -n monitoring port-forward svc/monitoring-grafana 3000:80 >/tmp/pf-grafana.log 2>&1 &
PF1=$!
kubectl -n monitoring port-forward svc/monitoring-kube-prometheus-prometheus 9090:9090 >/tmp/pf-prom.log 2>&1 &
PF2=$!
kubectl -n default port-forward svc/order-service 18080:80 >/tmp/pf-order.log 2>&1 &
PF3=$!
trap 'kill $PF1 $PF2 $PF3 2>/dev/null || true' EXIT INT TERM

echo "==> wait for grafana"
for _ in $(seq 1 30); do
  if curl -s --max-time 2 -o /dev/null http://localhost:3000/api/health; then break; fi
  sleep 0.8
done

URL='http://localhost:3000/d/k8s-obs-services?from=now-15m&to=now&refresh=10s'
cat <<EOF

==================================================
 Grafana    : http://localhost:3000
 Dashboard  : k8s-observability / Services
 User       : admin
 Password   : ${PWD_GRAFANA}
 Prometheus : http://localhost:9090/targets
 Traffic    : http://localhost:18080/order/100?userId=3
==================================================

Tip: bash scripts/load-traffic.sh in another terminal keeps the curves moving.
Press Ctrl+C here to stop the port-forwards.

EOF

if command -v start >/dev/null 2>&1; then
  start "" "$URL" >/dev/null 2>&1 || true
elif command -v open >/dev/null 2>&1; then
  open "$URL" || true
fi

wait
