# ADR: Series catalog + datapoint storage for OTLP metrics

Design decisions for the assignment in [`INSTRUCTION.md`](INSTRUCTION.md).

## 1. Problem and constraints

The scaffold stores every datapoint as a fully denormalized row: Resource attributes, Scope, metric name, description and unit are repeated next to every value. The assignment asks us to split this into a **lookup table** of metadata and lean datapoint rows (value + timestamp + reference). The schema still has to support efficient time-range queries at high ingest rates.

- Every query has a time range; no other filter is mandatory. No full table scans, **including on the lookup table**.
- Series cardinality is low, but Resource attributes change over time, so the series count keeps growing.
- High throughput: many messages and many datapoints per message.
- Production quality: consistent terminology, tests, debugging signals.
- **Assumption:** input comes from OTel SDKs and is well formed. No validation of datapoint content. Bad timestamps or values are the client's responsibility.
- **Out of scope:** retention/TTL (all data is kept); histogram, exponential histogram, summary (see [§8](#8-supported-metric-types)).

## 2. Solution overview

Each distinct combination of metric, Resource attributes and datapoint attributes is a **series**. It is identified by a deterministic 64-bit hash (`MetricHash`) computed in the service. The metadata of each series is written to a **catalog** table, once per day the series is active. Datapoints carry only `MetricHash`, timestamp and value.

Both tables put time first in their layout. The catalog is bucketed by day, so even metadata lookups are time-bounded. Datapoints are partitioned by day and sorted by hour bucket and then series, so time-only queries and per-series queries are both efficient. On ingest, catalog rows are written before the datapoints that reference them. An in-memory cache avoids rewriting known series.

```text
Export request
  ├─ map datapoints, compute MetricHash
  ├─ catalog rows not in cache ──► otel_metrics_series   (Day, MetricHash, metadata…)
  └─ datapoints ─────────────────► otel_metrics_gauge / otel_metrics_sum
                                    (TimeUnix, MetricHash, Value, …)
Query
  time range ──► datapoints ──JOIN on MetricHash──► catalog (same days)
```

## 3. Terminology

Use these terms consistently in code, logs, metrics and docs.

| Term | Meaning |
| --- | --- |
| Metric | What is measured: name, type, unit, description (e.g. `cpu.utilization`) |
| Resource | Telemetry source; `ResourceMetrics.Resource.Attributes` (e.g. `host.name`) |
| Scope | OTLP `InstrumentationScope`: the library that emits the metric |
| Series | One metric + Resource attributes + datapoint attributes (e.g. `cpu.utilization`, `host=web-1`, `cpu=0`) |
| Datapoint | One observation of a series: timestamp + value (+ start time, flags) |
| `MetricHash` | `UInt64` ID of a **series** (not of the metric definition). Join key between datapoints and catalog. |
| Catalog | `otel_metrics_series`: the lookup table holding series metadata |
| `DataPointAttributes` | `NumberDataPoint.Attributes`. Called `Attributes` in the current scaffold rows. |

## 4. Data model

### 4.1 Series identity (`MetricHash`)

The hash decides what counts as "the same series". Every field in it splits history when it changes, so it holds only what changes the *meaning* of the values. Descriptive fields stay in the catalog without affecting identity.

**Hashed fields:**
- `MetricName`, `MetricType`, `MetricUnit`
- `AggregationTemporality`, `IsMonotonic`. Always encoded; zero values for Gauge. A delta↔cumulative switch creates a new series.
- `ResourceAttributes`
- `DataPointAttributes`

**Not hashed** (descriptive catalog columns): Scope name/version/attributes, schema URLs, `MetricDescription`, `ServiceName` (already covered by `ResourceAttributes["service.name"]`), `ScopeDroppedAttrCount`.

**Canonical encoding.** The hash input is the same representation that is stored in the catalog, so two series with different hashes are always distinguishable when reading it:
- Attributes go through `map[string]string`, the same conversion used for the `Map` columns. If a key is duplicated, the last value wins.
- Keys are sorted within each attribute group. A separator byte goes between groups and fields.
- Every string is length-prefixed, so `"ab"+"c"` and `"a"+"bc"` hash differently.
- The same text with a different OTLP type (int `123` vs string `"123"`) gives **the same series**.
- Array, kvlist and bytes values keep the scaffold conversion (`fmt.Sprintf("%v", v)`). OTel SDKs send primitive metric attributes in practice, so the conversion is not changed until a real case needs it.

**Algorithm:** `xxhash64` (`github.com/cespare/xxhash/v2`, already an indirect dependency) producing `UInt64`. The collision risk is about 3·10⁻⁸ at 1M series, which is accepted. A golden test pins the hash of a known input.

**Consequences:**
- The same series sent from different scopes is one series. The catalog keeps the last scope seen that day. Two datapoints with the same `MetricHash` and `TimeUnix` can exist.
- Description changes do not split a series. The catalog keeps the last description per `(Day, MetricHash)`.
- **Known limitation:** array, kvlist and bytes attribute values are converted with the proto text format, which is not stable across builds and keeps kvlist keys in arrival order. A series with such an attribute can get a new `MetricHash` after an upgrade, or two `MetricHash` values for the same content in a different order. This is the only accepted exception to the "never hash `%v` output" rule in `AGENTS.md`. Fix path in [§10](#10-open-questions).

### 4.2 Catalog table (`otel_metrics_series`)

Without a time dimension, the catalog would only grow, because new hosts, pods and deployments keep creating new series. Searching it by name or attribute would then always be a full scan. Bucketing it by day keeps metadata lookups as time-bounded as datapoint reads.

A row means: **series `MetricHash` has at least one datapoint on `Day` (UTC)**.

```sql
CREATE TABLE otel_metrics_series (
    Day                    Date,
    MetricHash             UInt64,
    ServiceName            LowCardinality(String),
    MetricName             LowCardinality(String),
    MetricType             Enum8('gauge' = 1, 'sum' = 2),
    MetricUnit             String,
    MetricDescription      String,
    AggregationTemporality Int32,
    IsMonotonic            Bool,
    ResourceAttributes     Map(LowCardinality(String), String),
    ResourceSchemaUrl      String,
    ScopeName              String,
    ScopeVersion           String,
    ScopeAttributes        Map(LowCardinality(String), String),
    ScopeSchemaUrl         String,
    ScopeDroppedAttrCount  UInt32,
    DataPointAttributes    Map(LowCardinality(String), String)
) ENGINE = ReplacingMergeTree
PARTITION BY toYYYYMM(Day)
ORDER BY (Day, MetricHash)
```

- `Day = toDate(TimeUnix, 'UTC')` of the **datapoint**, not the ingestion time. A message that spans midnight produces one row per day.
- `Day` comes first in the sort key, so every catalog read is time-bounded.
- `MetricHash` follows `Day`, so `(Day, MetricHash)` is also the `ReplacingMergeTree` dedup key. No metadata column is in the key: the assignment defines no filter other than time, so we do not assume one. Filters on name, service or attributes read the catalog rows of the requested days, which is cheap because a day holds one row per active series.
- Monthly partitions, because the catalog is small. Daily partitions would create many tiny parts.

### 4.3 Datapoint tables (`otel_metrics_gauge`, `otel_metrics_sum`)

The datapoint layout has to serve two queries well: "everything in a time range" (the only guaranteed filter) and "these series in a time range" (after a catalog lookup). Neither pure time-first nor pure hash-first ordering does both. An hour bucket in front of the hash does.

```sql
CREATE TABLE otel_metrics_gauge (   -- otel_metrics_sum: same columns
    TimeUnix      DateTime64(9, 'UTC') CODEC(Delta, ZSTD(1)),
    MetricHash    UInt64,
    StartTimeUnix DateTime64(9, 'UTC') CODEC(Delta, ZSTD(1)),
    Value         Float64 CODEC(ZSTD(1)),
    Flags         UInt32 CODEC(ZSTD(1))
) ENGINE = MergeTree
PARTITION BY toDate(TimeUnix, 'UTC')
ORDER BY (toStartOfHour(TimeUnix, 'UTC'), MetricHash, TimeUnix)
```

- **Gauge and Sum stay in separate tables, even though their columns are identical.** `AggregationTemporality` and `IsMonotonic` are metric-level fields (on `Sum`, not on `NumberDataPoint`), so they live in the catalog. That leaves no type-specific column on the datapoints. We keep two tables because:
  - queries usually target one metric type, and the table choice is the type filter;
  - it keeps continuity with the scaffold and its tests.

  Trade-off: a query across all types needs `UNION ALL`, and each request does one insert per table. Merging them into one numeric table is a valid alternative (see §9).
- **Sort key: three options evaluated.**

  | `ORDER BY` | Time-only query | Series + time query | Compression |
  | --- | --- | --- | --- |
  | `(TimeUnix, MetricHash)` | reads only the range | reads every series in the range: ns timestamps make the hash useless as a second key | poor: series interleave |
  | `(MetricHash, TimeUnix)` | reads the whole day partition | optimal | optimal |
  | **`(toStartOfHour(TimeUnix), MetricHash, TimeUnix)`** ✅ | at most one extra hour | near optimal | near optimal |

  The chosen key groups rows by hour, then by series, then by time. A time-only query reads only the matching hours, because `toStartOfHour` is monotonic and a filter on `TimeUnix` uses the first key column. A series + time query jumps to the series block inside each hour. Values of one series are contiguous inside each hour, so they compress well.
- **Partition vs sort key.** The daily partition prunes whole days without opening their files. The hour bucket prunes granules inside a day. Hourly *partitions* would multiply parts (every insert creates at least one part per partition touched, and parts of different partitions never merge), which hurts compression and risks `TOO_MANY_PARTS`.
- The bucket size is tunable. Verify with `EXPLAIN indexes = 1`.

## 5. Ingestion

### 5.1 Write path

The order of the steps guarantees that a datapoint never references a series missing from the catalog. Each request does at most one catalog insert and one insert per datapoint table, with no per-datapoint lookups against ClickHouse.

Per `Export` request:
1. Map datapoints, compute `MetricHash`, and deduplicate catalog rows by `(Day, MetricHash)` within the message.
2. Drop keys already in the cache.
3. Insert the remaining catalog rows. **On failure, stop: no datapoints are inserted.**
4. Add the inserted keys to the cache (only after the insert succeeded).
5. Insert gauge and sum datapoints.

### 5.2 Throughput: async inserts

ClickHouse creates at least one part per insert. Many small `Export` requests, each doing synchronous inserts, would outpace merges and end in `TOO_MANY_PARTS`. Batching is left to ClickHouse instead of the service.

- Connection settings: `async_insert=1`, `wait_for_async_insert=1`.
- ClickHouse batches small inserts server-side.
- `Export` returns OK only when the data is persisted, so client retries stay correct.
- Cost: higher `Export` latency (`async_insert_busy_timeout_ms`).

### 5.3 Catalog cache

Without a cache, every message would rewrite all its series into the catalog. Correctness would not suffer (duplicates merge), but each message would pay an extra insert on the critical path. The cache does not affect correctness; it is needed for throughput.

- A set of `(Day, MetricHash)` with a mutex and a max size. When it is full, **clear it entirely**.
- Each instance rewrites its series once per day. That is what creates the new day's catalog row.
- An empty cache (after a restart, or across instances) causes duplicate catalog rows, which `ReplacingMergeTree` merges and reads dedupe. It never causes datapoints without metadata.

### 5.4 Consistency, errors and retries

There is no transaction across tables, so the guarantees are kept simple: never orphan datapoints, accept duplicates. The gRPC status codes must also tell OTLP clients when to retry.

- If metadata is written and the datapoints fail, the catalog has a row without datapoints.
- **At-least-once:** retries can duplicate datapoints. This is accepted.
- OTLP retryable codes: `Unavailable`, `DeadlineExceeded`, `Cancelled`, `Aborted`, `OutOfRange`, `DataLoss`. Returning a raw error produces `Unknown`, which is non-retryable, and the client drops the data.

| Situation | Response |
| --- | --- |
| ClickHouse error | `codes.Unavailable` |
| Context deadline exceeded / canceled | `DeadlineExceeded` / `Cancelled` |
| Unsupported metric types in the request | OK with `partial_success.rejected_data_points = N` and a message |

## 6. Querying

Every read starts from the time range. The catalog is always filtered by the matching days, and the datapoints by the exact range. Metadata filters narrow down the set of `MetricHash` values before the datapoints are read.

**Catalog read, deduplicated.** One clause handles both unmerged rows and multi-day rows:
```sql
SELECT ... FROM otel_metrics_series
WHERE Day BETWEEN toDate(a) AND toDate(b) [AND filters]
ORDER BY Day DESC
LIMIT 1 BY MetricHash
```
`FINAL` is not needed. The catalog returns a **superset** of the active series, and datapoints apply the exact time filter.

**Joins:** datapoints on the left (streamed), the deduplicated catalog on the right (in memory).
```sql
-- Time range only
SELECT d.TimeUnix, d.Value, s.MetricName, s.ResourceAttributes, s.DataPointAttributes
FROM otel_metrics_gauge AS d
INNER JOIN (<catalog read>) AS s USING MetricHash
WHERE d.TimeUnix BETWEEN a AND b

-- Time range + metadata filters: narrow the hashes first
WHERE d.TimeUnix BETWEEN a AND b
  AND d.MetricHash IN (
      SELECT MetricHash FROM otel_metrics_series
      WHERE Day BETWEEN toDate(a) AND toDate(b) AND MetricName = '...')
```

`Map` columns are not indexes. Attribute lookups are bounded by `Day` but not guaranteed fast within it. Skip indexes or dedicated columns are to be evaluated with real queries.

## 7. Observability

The scaffold already provides the OTel SDK, `otelgrpc` server instrumentation (RPC duration and status) and `slog` bridged to OTel. The signals below add the domain view: how much data flows in, what gets dropped, how the cache behaves and how ClickHouse responds.

Metric prefix: `com.dash0.homeexercise.`

| Metric | Type | Attributes | Purpose |
| --- | --- | --- | --- |
| `export.requests` | counter | `outcome` | Replaces `metrics.received`, which counts requests rather than metrics |
| `datapoints.received` | counter | `metric.type` | Inbound volume |
| `datapoints.dropped` | counter | `metric.type` | Unsupported types |
| `series.cache.lookups` | counter | `result` = `hit` / `miss` | Cache efficiency, rate of new series |
| `series.cache.resets` | counter | — | Cache cleared at max size |
| `clickhouse.insert.duration` | histogram | `table`, `outcome` | Insert latency and errors |
| `clickhouse.insert.rows` | counter | `table` | Rows written |

- **Traces:** child spans under the `otelgrpc` `Export` span: `map`, `insert.series`, `insert.gauge`, `insert.sum`, with series and datapoint counts as attributes.
- **Logs (`slog`):**
  - `Error`: failed insert (table, row count, error).
  - `Warn`: unsupported types, aggregated per request.
  - `Debug`: per-request summary.

## 8. Supported metric types

The assignment starts from Gauge and Sum being stored correctly and asks to restructure that storage. "Value + timestamp" describes number datapoints, not distributions. Other types are therefore out of scope, but they are reported instead of being silently dropped as they are today.

- **Implemented:** Gauge and Sum.
- **Removed:** the scaffold's histogram, exponential histogram and summary tables. They are created but never written; the mapper drops those types silently today.
- **Extension path:** shared catalog and `MetricHash`, plus one datapoint table per type. Decisions for that time:
  - `ExplicitBounds`: put them in identity and catalog. They are fixed per series, and a change makes bucket counts incomparable.
  - Exponential histogram `Scale`: keep it per datapoint, because SDKs change it dynamically.
  - Optional `Sum`/`Min`/`Max`: `Nullable` columns or a sentinel value.
  - Priority: Histogram (common in OTel semconv, e.g. `http.server.request.duration`), then Exponential histogram, then Summary.

## 9. Alternatives considered

| Alternative | Why rejected / deferred |
| --- | --- |
| Catalog without a time dimension | Metadata lookups become full scans as Resources churn |
| Separate Resource table with its own hash | Extra insert and lookup. Resource attributes repeat per series, not per datapoint. |
| Hourly partitions | Too many parts |
| Single datapoint table for Gauge + Sum | Fewer inserts, and time-only queries read one table. Rejected for now: queries usually target one type, and separate tables keep the type filter free and the scaffold structure intact. The type would live in the catalog (`MetricType`), optionally also as an `Enum8` column on the datapoints. |
| In-service write buffer | Flush goroutines, backpressure, shutdown complexity; data-loss risk if OK is returned before the flush |
| `AggregatingMergeTree` with `FirstSeen`/`LastSeen` | Incompatible with the cache: `LastSeen` stays at the first write of the day. Day granularity is enough for a superset. |
| Hashing the typed OTLP values | Would create series that look identical in the `Map(String, String)` catalog |
| Scope in identity | Same measurement from different libraries is treated as one series |

## 10. Open questions

- **Deterministic conversion of array, kvlist and bytes attributes** (e.g. JSON with sorted keys, base64 for bytes) if such attributes show up in metrics.
- **LRU cache** (e.g. `hashicorp/golang-lru`) if `series.cache.resets` is frequent.
- **Skip indexes / dedicated columns** for frequent attribute filters.
- **Datapoint deduplication** to move beyond at-least-once.
- **Observability** section to be detailed further.
