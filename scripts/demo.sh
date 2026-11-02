#!/usr/bin/env bash
# Local end-to-end demo of the OTLP metrics backend.
#
#   telemetrygen ──OTLP gRPC :4317──▶ service ──native :9000──▶ ClickHouse
#                                        │                      otel_metrics_catalog
#                                        │                      otel_metrics_gauge
#                                        │                      otel_metrics_sum
#                                        └──OTLP gRPC :18889──▶ Aspire Dashboard (UI :18888)
#
# Steps:
#   1. Start ClickHouse and the Aspire Dashboard; drop the service tables so they
#      are recreated with the current schema.
#   2. Build and start the service, exporting its own metrics, logs and traces
#      to the dashboard.
#   3. Send Sum and Gauge datapoints for two services, plus a Histogram, which
#      the service rejects.
#   4. Print the stored Sum and Gauge values.
#   5. Print how to look at the data, and keep the service running for the
#      dashboard until Ctrl+C.
set -euo pipefail

cd "$(dirname "$0")/.."

export CLICKHOUSE_PASSWORD="${CLICKHOUSE_PASSWORD:-dash0}"
TELEMETRYGEN="github.com/open-telemetry/opentelemetry-collector-contrib/cmd/telemetrygen@v0.120.0"
SERVICES=(checkout payments)
DATAPOINTS=5
WORK_DIR="$(mktemp -d)"
APP_PID=""

cleanup() {
  if [[ -n "$APP_PID" ]] && kill -0 "$APP_PID" 2>/dev/null; then
    echo "==> Stopping the service"
    kill "$APP_PID"
    wait "$APP_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK_DIR"
  echo "ClickHouse and the dashboard are still running. Stop them with:"
  echo "  docker compose --profile observability down"
}
trap cleanup EXIT
trap "exit 130" INT TERM

clickhouse_query() {
  docker compose exec -T clickhouse clickhouse-client --password "$CLICKHOUSE_PASSWORD" --format PrettyCompact -q "$1"
}

port_open() {
  (echo >"/dev/tcp/localhost/$1") 2>/dev/null
}

wait_for() {
  local description="$1"
  shift
  for _ in $(seq 1 60); do
    if "$@" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "Timed out waiting for $description" >&2
  exit 1
}

send() {
  local service="$1" metric_type="$2"
  go run "$TELEMETRYGEN" metrics --otlp-insecure --otlp-endpoint localhost:4317 \
    --metrics "$DATAPOINTS" --metric-type "$metric_type" \
    --otlp-attributes "service.name=\"$service\"" \
    >"$WORK_DIR/telemetrygen.log" 2>&1
  echo "    $service: $DATAPOINTS $metric_type datapoints"
}

stored_values() {
  local metric_type="$1" table="$2"
  clickhouse_query "
    WITH series AS (
        SELECT MetricHash, ServiceName, MetricName
        FROM otel_metrics_catalog
        WHERE Day BETWEEN toDate(now() - INTERVAL 15 MINUTE, 'UTC') AND toDate(now(), 'UTC')
          AND MetricType = '$metric_type'
        ORDER BY Day DESC
        LIMIT 1 BY MetricHash
    )
    SELECT s.ServiceName, s.MetricName, d.TimeUnix, d.Value
    FROM $table AS d
    INNER JOIN series AS s USING MetricHash
    WHERE d.MetricHash IN (SELECT MetricHash FROM series)
      AND d.TimeUnix >= now() - INTERVAL 15 MINUTE
    ORDER BY s.ServiceName, d.TimeUnix"
}

echo "==> Starting ClickHouse and the Aspire Dashboard"
docker compose --profile observability up -d
wait_for "ClickHouse" clickhouse_query "SELECT 1"
for table in otel_metrics_catalog otel_metrics_gauge otel_metrics_sum; do
  clickhouse_query "DROP TABLE IF EXISTS $table"
done

echo "==> Starting the service (log: $WORK_DIR/service.log)"
if port_open 4317; then
  echo "Port 4317 is already in use: stop the other OTLP receiver first" >&2
  exit 1
fi
go build -o "$WORK_DIR/service" .
OTEL_METRICS_EXPORTER=otlp \
OTEL_LOGS_EXPORTER=otlp \
OTEL_TRACES_EXPORTER=otlp \
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:18889 \
OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
OTEL_METRIC_EXPORT_INTERVAL=5000 \
  "$WORK_DIR/service" >"$WORK_DIR/service.log" 2>&1 &
APP_PID=$!
wait_for "the service on port 4317" port_open 4317
if ! kill -0 "$APP_PID" 2>/dev/null; then
  echo "The service exited at startup:" >&2
  cat "$WORK_DIR/service.log" >&2
  exit 1
fi

echo "==> Sending metrics with telemetrygen"
for service in "${SERVICES[@]}"; do
  send "$service" Sum
  send "$service" Gauge
done
send "${SERVICES[0]}" Histogram
echo "    (Histogram is not stored: only Gauge and Sum are supported, the service answers with a partial success)"

echo
echo "==> Sum values stored in ClickHouse"
stored_values sum otel_metrics_sum

echo
echo "==> Gauge values stored in ClickHouse"
stored_values gauge otel_metrics_gauge

echo
echo "==> How to look at the data"
echo "    ClickHouse: docker compose exec clickhouse clickhouse-client --password $CLICKHOUSE_PASSWORD"
echo "    Dashboard:  http://localhost:18888 (Metrics > otlp-metrics-processor-backend)"
echo "    The service keeps running; press Ctrl+C to stop it."
wait "$APP_PID"
