#!/bin/bash

# Script to verify that the federate scrape job is running in Prometheus

if [ -z "$MANAGEMENT_KUBECONFIG" ]; then
  echo "Error: MANAGEMENT_KUBECONFIG variable is not set"
  exit 1
fi

echo "Verifying federate scrape job in Prometheus..."

# Start port-forward in background
kubectl port-forward -n monitoring svc/kind-prometheus-kube-prome-prometheus 9090:9090 --kubeconfig $MANAGEMENT_KUBECONFIG > /dev/null 2>&1 &
PF_PID=$!

# Wait for port-forward to be ready
sleep 3

# Check if port-forward is still running
if ! kill -0 $PF_PID 2>/dev/null; then
  echo "Error: Failed to establish port-forward to Prometheus"
  exit 1
fi

# Query Prometheus targets
TARGETS=$(curl -s http://localhost:9090/api/v1/targets 2>/dev/null)

# Kill port-forward
kill $PF_PID 2>/dev/null
wait $PF_PID 2>/dev/null

# Check if we got a response
if [ -z "$TARGETS" ]; then
  echo "Error: Failed to query Prometheus API"
  exit 1
fi

# Parse JSON and check for federate job
FEDERATE_UP=$(echo "$TARGETS" | jq -r '.data.activeTargets[] | select(.scrapePool | contains("federate")) | .health' 2>/dev/null)

if [ -z "$FEDERATE_UP" ]; then
  echo "❌ Federate scrape job not found in active targets"
  echo "Available jobs:"
  echo "$TARGETS" | jq -r '.data.activeTargets[] | .scrapePool' | sort -u
  exit 1
fi

if [ "$FEDERATE_UP" = "up" ]; then
  echo "✅ Federate scrape job is UP and running"
  # Show details
  echo "$TARGETS" | jq -r '.data.activeTargets[] | select(.scrapePool | contains("federate")) | "  Job: \(.labels.job)\n  Instance: \(.scrapeUrl)\n  Health: \(.health)\n  Last Scrape: \(.lastScrape)"'
  exit 0
else
  echo "❌ Federate scrape job is DOWN"
  echo "Details:"
  echo "$TARGETS" | jq -r '.data.activeTargets[] | select(.scrapePool | contains("federate")) | "  Job: \(.labels.job)\n  Health: \(.health)\n  Last Error: \(.lastError)"'
  exit 1
fi
