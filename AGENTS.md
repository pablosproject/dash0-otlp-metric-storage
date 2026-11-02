# Agent instructions

Instructions for any agent (human or AI) working in this repository.

## Project

A Go gRPC backend that receives OTLP metrics and stores them in ClickHouse, split into a series catalog (metadata) and lean datapoint tables. It is a take-home assignment with a 3–4 hour budget, built to production standards: prefer simple, working, tested code over completeness.

## Read first

| Document | Purpose |
| --- | --- |
| [`docs/INSTRUCTION.md`](docs/INSTRUCTION.md) | The assignment: requirements and constraints |
| [`docs/ADR-Home-Assignment.md`](docs/ADR-Home-Assignment.md) | **Source of truth for the design**: schema, series identity, write path, read patterns, error handling, observability, scope |
| [`README.md`](README.md) | How to build, run and test |

- Implement what the ADR decides. Do not re-propose alternatives listed in its "Alternatives considered" section.
- If the code needs to deviate from the ADR, stop and ask. Once agreed, update the ADR in the same change.
- Use the ADR terminology everywhere (code, logs, metric names, comments, tests): *series*, *datapoint*, *catalog*, `MetricHash`, `DataPointAttributes`. Do not introduce synonyms.

## Workflow

1. **Research:** read the relevant code and ADR sections.
2. **Plan:** propose the change and wait for confirmation before implementing anything non-trivial.
3. **Implement** in small steps, each one building and passing tests.
4. **Verify** before declaring done: `make build`, `make lint`, `make test`, and `make test-integration` when storage or the gRPC flow is touched.

## Commands

| Command | Description |
| --- | --- |
| `make build` | Compile |
| `make lint` | `go vet` + `staticcheck` (if installed) |
| `make test` | Unit tests |
| `make test-integration` | Integration tests against real ClickHouse via testcontainers (needs Docker running) |
| `make test-all` | Unit + integration tests |
| `make fmt` | `go fmt` |

## Code conventions

- Go 1.26, standard library first. Add a dependency only when it clearly pays off, and say why.
- Simple and explicit over clever. No speculative abstractions.
- Descriptive variable names, as clear as possible: `resourceMetrics` not `rm`, `datapoint` not `dp`, `serviceName` not `svcName`, `resourceAttributes` not `resAttrs`. Single-letter or abbreviated names only where Go idiom expects them: `i`, `err`, `ctx`, `t`, method receivers.
- Comments only where the code is critical or genuinely non-obvious. Do not use comments to explain a change or a fix; that belongs in the commit message.
- Wrap errors with context (`fmt.Errorf("inserting catalog rows: %w", err)`).
- Logging through `slog` (bridged to OTel). Structured fields, no string concatenation.
- Never log or hash with `fmt.Sprintf("%v", protoMessage)`: the proto text format is not stable.

## Tests

- Unit tests: `*_test.go`, no build tag, no external dependencies (use a fake `MetricsStore`).
- Integration tests: `//go:build integration` tag; ClickHouse is started with testcontainers.
- Prefer table-driven tests. Every behaviour change comes with a test.
- Verify "no full table scan" claims with `EXPLAIN indexes = 1` in integration tests rather than asserting them in prose.

## Commits

[Conventional Commits](https://www.conventionalcommits.org/):

```text
<type>(<optional scope>): <short imperative summary>

<optional body: why, for non-trivial changes>
```

- Types: `feat`, `fix`, `refactor`, `test`, `docs`, `build`, `chore`, `perf`.
- Scopes (optional): `catalog`, `datapoints`, `hash`, `ingest`, `query`, `otel`, `schema`.
- One logical change per commit. The summary is one short sentence; add a body only for complex changes.
- No AI attribution of any kind: no `Co-Authored-By` trailers, no "Generated with" lines, in commits, PR descriptions or code.
- Commit or push only when asked.

Examples:

```text
feat(catalog): add otel_metrics_series table and insert path
fix(hash): length-prefix strings to avoid boundary collisions
docs: document hour-bucket sort key in ADR
```
