# OTLP Metric Storage

A gRPC backend that receives OpenTelemetry metrics (OTLP) and stores them in ClickHouse.

- Assignment: [`docs/INSTRUCTION.md`](docs/INSTRUCTION.md)
- Design decisions: [`docs/ADR-Home-Assignment.md`](docs/ADR-Home-Assignment.md)

## Current state

- **Storage:** Gauge and Sum datapoints are stored as lean rows (`otel_metrics_gauge`, `otel_metrics_sum`) that reference the series catalog (`otel_metrics_series`) by `MetricHash`. Catalog rows are written before the datapoints. Covered by unit and integration tests.
- **Catalog cache:** each instance writes a series to the catalog once per day; known `(Day, MetricHash)` keys are skipped. The cache is updated only after a successful catalog insert.
- **Not implemented yet** (see the ADR): gRPC status codes and partial success, async inserts, ClickHouse wiring in `main.go`, domain observability.

## Requirements

- Go 1.26+
- Docker (for the integration tests)

## Usage

Build the application:
```shell
go build ./...
```

Run the application:
```shell
go run .
```

Run tests:
```shell
go test ./...
```

Run integration tests (starts ClickHouse via testcontainers):
```shell
make test-integration
```
