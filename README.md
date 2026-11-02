# OTLP Metric Storage

A gRPC backend that receives OpenTelemetry metrics (OTLP) and stores them in ClickHouse.

- Assignment: [`docs/INSTRUCTION.md`](docs/INSTRUCTION.md)
- Design decisions: [`docs/ADR-Home-Assignment.md`](docs/ADR-Home-Assignment.md)

## Current state

- **Storage:** Gauge and Sum datapoints are stored as lean rows (`otel_metrics_gauge`, `otel_metrics_sum`) that reference the series catalog (`otel_metrics_catalog`) by `MetricHash`. Catalog rows are written before the datapoints. Covered by unit and integration tests.
- **Catalog cache:** each instance writes a series to the catalog once per day; known `(Day, MetricHash)` keys are skipped. The cache is updated only after a successful catalog insert.
- **Async inserts:** ClickHouse batches small inserts server-side; `Export` returns only once the data is persisted. See [Async inserts](#async-inserts).
- **gRPC status codes:** storage errors are returned as `Unavailable`, `DeadlineExceeded` or `Canceled`, which OTLP clients retry. The error detail is logged, not sent to the client.
- **Partial success:** unsupported metric types (histogram, exponential histogram, summary) are not stored; `Export` returns OK with `partial_success.rejected_data_points` and the count per type.
- **Observability:** domain metrics and logs exported via OTLP or stdout. See [Service metrics and logs](#service-metrics-and-logs).

## Requirements

- Go 1.26+
- Docker (for ClickHouse and the integration tests)

## Quick demo

One command runs the whole flow end to end:
```shell
make demo   # or ./scripts/demo.sh
```

```text
telemetrygen ──OTLP gRPC :4317──▶ service ──native :9000──▶ ClickHouse
                                     │                      catalog, gauge, sum
                                     └──OTLP gRPC :18889──▶ Aspire Dashboard (UI :18888)
```

1. Starts ClickHouse and the Aspire Dashboard, and drops the service tables so they are recreated with the current schema.
2. Builds and starts the service, exporting its own metrics, logs and traces to the dashboard.
3. Sends 5 Sum and 5 Gauge datapoints for two services (`checkout`, `payments`), plus 5 Histogram datapoints, which are rejected with a partial success.
4. Prints the Sum and Gauge values stored in ClickHouse.
5. Prints how to look at the data, and keeps the service running for the dashboard until Ctrl+C.

It takes about 15 seconds (longer the first time, while `go run` builds `telemetrygen`). The sections below do it by hand.

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

-- All Sum values of the last hour: series from the catalog first, then their datapoints (ADR §6)
WITH series AS (
    SELECT MetricHash, ServiceName, MetricName
    FROM otel_metrics_catalog
    WHERE Day BETWEEN toDate(now() - INTERVAL 1 HOUR, 'UTC') AND toDate(now(), 'UTC')
      AND MetricType = 'sum'
    ORDER BY Day DESC
    LIMIT 1 BY MetricHash
)
SELECT s.ServiceName, s.MetricName, d.TimeUnix, d.Value
FROM otel_metrics_sum AS d
INNER JOIN series AS s USING MetricHash
WHERE d.MetricHash IN (SELECT MetricHash FROM series)
  AND d.TimeUnix >= now() - INTERVAL 1 HOUR
ORDER BY s.ServiceName, d.TimeUnix;
```

## Stop

Stop ClickHouse (and the dashboard) and drop their data:
```shell
docker compose --profile observability down
```

## Evaluating the service

How to check that the service is correct, observable and efficient, and what was done to evaluate it.

### Correctness: tests

| Command | Description |
| --- | --- |
| `make build` | Compile |
| `make lint` | `go vet` + `staticcheck` (if installed) |
| `make test` | Unit tests, no external dependencies |
| `make test-integration` | Integration tests against a real ClickHouse started with testcontainers (needs Docker) |

- **Unit tests** cover the series identity (`MetricHash` golden values, attribute order and type), the OTLP → row mapping, the catalog cache, and the `Export` write path and status codes against a fake store.
- **Integration tests** create the schema, insert catalog rows and datapoints, and send an OTLP request through gRPC down to ClickHouse.

### Service metrics and logs

The service exports metrics, logs and the `otelgrpc` `Export` spans through the OpenTelemetry SDK. The exporter is chosen with the standard `OTEL_*` environment variables (`OTEL_METRICS_EXPORTER`, `OTEL_LOGS_EXPORTER`, `OTEL_TRACES_EXPORTER`, `OTEL_EXPORTER_OTLP_*`); when they are unset, everything is printed to stdout. Set `OTEL_TRACES_EXPORTER=none` to turn the spans off, or `OTEL_TRACES_SAMPLER` to sample them.

| Metric (`com.dash0.homeexercise.` prefix) | Attributes | What it tells you |
| --- | --- | --- |
| `export.requests` | `outcome` = `success` / `partial_success` / `failure` | Request rate and how requests end |
| `datapoints.received` | `metric.type` | Inbound volume per metric type |
| `datapoints.dropped` | `metric.type` | Datapoints of unsupported types (histogram, exponential histogram, summary) |
| `catalog.cache.lookups` | `result` = `hit` / `miss` | Cache efficiency; misses are new series per day |
| `catalog.cache.resets` | — | Cache cleared because it was full; frequent resets mean `maxSize` is too small |
| `clickhouse.insert.duration` | `table`, `outcome` | Insert latency (including the async insert flush) and errors per table |
| `clickhouse.insert.rows` | `table` | Rows written per table |

Questions they answer:

- *Is the catalog cache working?* `catalog.cache.lookups`: after the first Export of the day, almost all lookups are hits.
- *Is ClickHouse keeping up?* `clickhouse.insert.duration` per table, and `export.requests` with `outcome=failure`.
- *Are clients sending data we drop?* `datapoints.dropped` per type, and the matching `Warn` logs.

Logs: `Error` for a failed insert (table, row count, error), `Warn` for rejected unsupported datapoints. `otelgrpc` adds RPC duration and status metrics.

**Watch them locally.** The [Aspire Dashboard](https://learn.microsoft.com/dotnet/aspire/fundamentals/dashboard/standalone) is a single container that receives OTLP and shows metrics and logs. It is behind a compose profile:
```shell
docker compose --profile observability up -d
```

Run the application exporting to it (metrics every 5 seconds):
```shell
CLICKHOUSE_PASSWORD=dash0 \
OTEL_METRICS_EXPORTER=otlp OTEL_LOGS_EXPORTER=otlp OTEL_TRACES_EXPORTER=otlp \
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:18889 OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
OTEL_METRIC_EXPORT_INTERVAL=5000 \
go run .
```

Send data as in [Try it](#try-it), plus a histogram to see `datapoints.dropped` and the partial success response:
```shell
go run github.com/open-telemetry/opentelemetry-collector-contrib/cmd/telemetrygen@v0.120.0 metrics \
  --otlp-insecure --otlp-endpoint localhost:4317 \
  --metrics 5 --metric-type Histogram
```

Open http://localhost:18888 and pick the `otlp-metrics-processor-backend` resource under **Metrics**.

### Query performance

`make query-plan` measures how the schema serves typical reads (ADR §4.3). It starts ClickHouse, creates the tables with the service's own DDL in a scratch database (`query_plan`, so `default` is not touched), loads synthetic data (2000 series, one datapoint every 30s for three days, 17.3M rows) and runs two catalog-first queries:

1. one metric across all services, per-minute average over one hour;
2. latest value of every series of one service over one hour.

For each query it prints the SQL and two measurements:

- **`EXPLAIN indexes = 1`**, per table read: parts and granules kept by each index, in order. `MinMax` and `Partition` drop the days outside the range; `PrimaryKey` drops the granules of other series inside the day.
- **Execution**, from `system.query_log`: rows and bytes actually read, parts, granules and duration.

This is how the datapoint sort key was chosen. The first version, `(toStartOfHour(TimeUnix), MetricHash, TimeUnix)`, read the whole day for both queries; `(MetricHash, TimeUnix)` reads 3 to 5 times fewer rows:

| Query | Granules read (of 704 in the day) | Rows read |
| --- | --- | --- |
| One metric, all services | 693 → **133** | 5.69M → **1.11M** |
| One service, all series | 704 → **227** | 5.78M → **1.88M** |

Details in ADR §4.3. Rerun the script after any change to the schema and compare the numbers.
