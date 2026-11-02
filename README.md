# OTLP Metric Storage

A gRPC backend that receives OpenTelemetry metrics (OTLP) and stores them in ClickHouse.

- Assignment: [`docs/INSTRUCTION.md`](docs/INSTRUCTION.md)
- Design decisions: [`docs/ADR-Home-Assignment.md`](docs/ADR-Home-Assignment.md)

## Current state

- **Storage:** Gauge and Sum datapoints are stored as lean rows (`otel_metrics_gauge`, `otel_metrics_sum`) that reference the series catalog (`otel_metrics_catalog`) by `MetricHash`. Catalog rows are written before the datapoints. Covered by unit and integration tests.
- **Catalog cache:** each instance writes a series to the catalog once per day; known `(Day, MetricHash)` keys are skipped. The cache is updated only after a successful catalog insert.
- **Async inserts:** ClickHouse batches small inserts server-side; `Export` returns only once the data is persisted. See [Async inserts](#async-inserts).
- **Not implemented yet** (see the ADR): gRPC status codes and partial success, domain observability.

## Requirements

- Go 1.26+
- Docker (for ClickHouse and the integration tests)

## Run locally

Start ClickHouse:
```shell
docker compose up -d
```

Run the application (the tables are created at startup):
```shell
CLICKHOUSE_PASSWORD=dash0 go run .
```

| Flag | Default | Description |
| --- | --- | --- |
| `-listenAddr` | `localhost:4317` | OTLP gRPC listen address |
| `-maxReceiveMessageSize` | `16777216` | Max gRPC message size in bytes |
| `-clickhouseAddr` | `localhost:9000` | ClickHouse native protocol address |
| `-clickhouseDatabase` | `default` | ClickHouse database |
| `-clickhouseUsername` | `default` | ClickHouse username |

The ClickHouse password is read from the `CLICKHOUSE_PASSWORD` environment variable, so it does not show up in the process list. `docker compose` uses `dash0` unless `CLICKHOUSE_PASSWORD` is set.

`SIGINT`/`SIGTERM` stop the server gracefully: in-flight `Export` calls complete before the ClickHouse connection is closed.

## Async inserts

The service does not buffer datapoints itself. Every `Export` request inserts its rows straight away, and ClickHouse is asked to batch those inserts on the server (ADR §5.2).

**Why:** ClickHouse creates at least one data part per `INSERT`. With many small `Export` requests, synchronous inserts would create parts faster than the background merges can combine them, and inserts would start failing with `TOO_MANY_PARTS`. An in-service buffer would avoid that, but it adds flush goroutines, backpressure and shutdown handling, and risks losing data if `OK` is returned before the flush (ADR §9).

**How:** the connection sets two settings:

| Setting | Value | Effect |
| --- | --- | --- |
| `async_insert` | `1` | ClickHouse collects small inserts in an in-memory buffer and writes them as one part |
| `wait_for_async_insert` | `1` | The insert, and therefore `Export`, returns only after the buffer has been flushed to disk |

**Guarantee:** `Export` returns `OK` only when the data is persisted. If ClickHouse fails, the client gets an error and retries, so delivery stays at-least-once.

**Cost:** each `Export` can wait up to the server's flush timeout (`async_insert_busy_timeout_ms`) before it returns. This adds latency, not load.

**Check it:** after sending some metrics (see [Try it](#try-it)):
```sql
SYSTEM FLUSH LOGS;
SELECT table, status, count()
FROM system.asynchronous_insert_log
GROUP BY table, status;
```

## Try it

Send sample metrics with [`telemetrygen`](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/cmd/telemetrygen), the OpenTelemetry load generator:
```shell
go run github.com/open-telemetry/opentelemetry-collector-contrib/cmd/telemetrygen@v0.120.0 metrics \
  --otlp-insecure --otlp-endpoint localhost:4317 \
  --metrics 5 --workers 2 --metric-type Sum \
  --otlp-attributes 'service.name="telemetrygen-demo"'

go run github.com/open-telemetry/opentelemetry-collector-contrib/cmd/telemetrygen@v0.120.0 metrics \
  --otlp-insecure --otlp-endpoint localhost:4317 \
  --metrics 5 --metric-type Gauge
```

Inspect what was stored:
```shell
docker compose exec clickhouse clickhouse-client --password dash0
```

```sql
-- One catalog row per series and day; datapoints only carry MetricHash, time and value
SELECT Day, MetricHash, ServiceName, MetricName, MetricType, AggregationTemporality, IsMonotonic
FROM otel_metrics_catalog;

-- Read datapoints for a time range and join their metadata from the catalog (ADR §6)
SELECT s.ServiceName, s.MetricName, d.TimeUnix, d.Value
FROM otel_metrics_sum AS d
INNER JOIN (
    SELECT MetricHash, ServiceName, MetricName
    FROM otel_metrics_catalog
    WHERE Day BETWEEN toDate(now() - INTERVAL 1 HOUR) AND today()
    ORDER BY Day DESC
    LIMIT 1 BY MetricHash
) AS s USING MetricHash
WHERE d.TimeUnix >= now() - INTERVAL 1 HOUR
ORDER BY d.TimeUnix;
```

Stop ClickHouse and drop its data:
```shell
docker compose down
```

## Tests

| Command | Description |
| --- | --- |
| `make build` | Compile |
| `make lint` | `go vet` + `staticcheck` (if installed) |
| `make test` | Unit tests |
| `make test-integration` | Integration tests against real ClickHouse via testcontainers (needs Docker running) |
