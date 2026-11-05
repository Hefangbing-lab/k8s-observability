#!/usr/bin/env bash
# load-traffic.sh -- send steady traffic so the Grafana panels are not flat.
# Usage: bash scripts/load-traffic.sh [count] [base-url]
set -euo pipefail

COUNT="${1:-300}"
BASE="${2:-http://localhost:18080}"
ok=0
fail=0

echo "==> sending $COUNT requests to $BASE"
for i in $(seq 1 "$COUNT"); do
  uid=$(( i % 5 + 1 ))
  code=$(curl -s --max-time 5 -o /dev/null -w '%{http_code}' "$BASE/order/100?userId=$uid" || echo 000)
  if [ "$code" = "200" ]; then ok=$((ok+1)); else fail=$((fail+1)); fi
  if [ $(( i % 50 )) -eq 0 ]; then echo "  $i sent (ok=$ok fail=$fail)"; fi
  sleep 0.1
done

echo "done: ok=$ok fail=$fail"
echo "Open: http://localhost:3000/d/k8s-obs-services?from=now-15m&to=now&refresh=10s"
