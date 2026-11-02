package metrics

const createCatalogTableSQL = `
CREATE TABLE IF NOT EXISTS otel_metrics_catalog (
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
ORDER BY (Day, MetricHash);
`

const createGaugeTableSQL = `
CREATE TABLE IF NOT EXISTS otel_metrics_gauge (
    TimeUnix      DateTime64(9, 'UTC') CODEC(Delta, ZSTD(1)),
    MetricHash    UInt64,
    StartTimeUnix DateTime64(9, 'UTC') CODEC(Delta, ZSTD(1)),
    Value         Float64 CODEC(ZSTD(1)),
    Flags         UInt32 CODEC(ZSTD(1))
) ENGINE = MergeTree
PARTITION BY toDate(TimeUnix, 'UTC')
ORDER BY (toStartOfHour(TimeUnix, 'UTC'), MetricHash, TimeUnix)
SETTINGS ttl_only_drop_parts = 1;
`

const createSumTableSQL = `
CREATE TABLE IF NOT EXISTS otel_metrics_sum (
    TimeUnix      DateTime64(9, 'UTC') CODEC(Delta, ZSTD(1)),
    MetricHash    UInt64,
    StartTimeUnix DateTime64(9, 'UTC') CODEC(Delta, ZSTD(1)),
    Value         Float64 CODEC(ZSTD(1)),
    Flags         UInt32 CODEC(ZSTD(1))
) ENGINE = MergeTree
PARTITION BY toDate(TimeUnix, 'UTC')
ORDER BY (toStartOfHour(TimeUnix, 'UTC'), MetricHash, TimeUnix)
SETTINGS ttl_only_drop_parts = 1;
`
