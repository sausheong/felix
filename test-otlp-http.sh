#!/usr/bin/env bash
# Send a sample OTLP/HTTP trace to the collector NLB and report the result.
# Usage: ./test-otlp-http.sh [endpoint]

set -euo pipefail

ENDPOINT="${1:-http://aiap-nprd-otel-inet-nlb-6752644d1ca24fad.elb.ap-southeast-1.amazonaws.com:4318}"
URL="${ENDPOINT%/}/v1/traces"

# OTLP uses 16-byte trace IDs and 8-byte span IDs as hex strings.
TRACE_ID=$(openssl rand -hex 16)
SPAN_ID=$(openssl rand -hex 8)

# Nanoseconds since epoch (macOS date doesn't support %N, so use python).
NOW_NS=$(python3 -c 'import time; print(time.time_ns())')
START_NS=$((NOW_NS - 1000000000))

PAYLOAD=$(cat <<EOF
{
  "resourceSpans": [{
    "resource": {
      "attributes": [
        {"key": "service.name", "value": {"stringValue": "otlp-nlb-smoketest"}},
        {"key": "host.name",    "value": {"stringValue": "$(hostname)"}}
      ]
    },
    "scopeSpans": [{
      "scope": {"name": "test-otlp-http.sh"},
      "spans": [{
        "traceId": "${TRACE_ID}",
        "spanId":  "${SPAN_ID}",
        "name": "smoketest-span-sausheong",
        "kind": 1,
        "startTimeUnixNano": "${START_NS}",
        "endTimeUnixNano":   "${NOW_NS}",
        "attributes": [
          {"key": "test.run", "value": {"stringValue": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"}}
        ],
        "status": {"code": 1}
      }]
    }]
  }]
}
EOF
)

echo "POST  $URL"
echo "trace ${TRACE_ID}  span ${SPAN_ID}"
echo

HTTP_CODE=$(curl -sS -o /tmp/otlp-resp.$$ -w '%{http_code}' \
  -X POST "$URL" \
  -H 'Content-Type: application/json' \
  --data-binary "$PAYLOAD" \
  --max-time 10) || {
    echo "curl failed: cannot reach $URL"
    rm -f /tmp/otlp-resp.$$
    exit 1
  }

BODY=$(cat /tmp/otlp-resp.$$)
rm -f /tmp/otlp-resp.$$

echo "HTTP $HTTP_CODE"
[ -n "$BODY" ] && echo "body: $BODY"

case "$HTTP_CODE" in
  200|202) echo "OK — collector accepted the trace." ; exit 0 ;;
  *)       echo "FAIL — unexpected status." ; exit 1 ;;
esac
