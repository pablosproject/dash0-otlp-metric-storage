# OTLP Metric Storage

A gRPC backend that receives OpenTelemetry metrics (OTLP) and stores them in ClickHouse.

- Assignment: [`docs/INSTRUCTION.md`](docs/INSTRUCTION.md)
- Design decisions: [`docs/ADR-Home-Assignment.md`](docs/ADR-Home-Assignment.md)

## Current state

- **Scaffold:** a gRPC endpoint receives metrics; Gauge and Sum datapoints are converted to denormalized rows and inserted into ClickHouse. Covered by unit and integration tests.
- **Design:** the split into a series catalog and lean datapoint tables is decided in the ADR.
- **Implementation:** not started yet.

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
go run ./...
```

Run tests:
```shell
go test ./...
```

Run integration tests (starts ClickHouse via testcontainers):
```shell
make test-integration
```
