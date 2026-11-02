// Package telemetry holds the domain metrics and logs of the OTLP ingest path.
// Instrument names share the com.dash0.homeexercise. prefix.
package telemetry

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	instrumentationName = "dash0.com/otlp-log-processor-backend"
	metricPrefix        = "com.dash0.homeexercise."
)

type Outcome string

const (
	OutcomeSuccess        Outcome = "success"
	OutcomePartialSuccess Outcome = "partial_success"
	OutcomeFailure        Outcome = "failure"
)

var (
	meter = otel.Meter(instrumentationName)

	exportRequests       = int64Counter("export.requests", "Export requests by outcome", "{request}")
	datapointsReceived   = int64Counter("datapoints.received", "Datapoints received, by metric type", "{datapoint}")
	datapointsDropped    = int64Counter("datapoints.dropped", "Datapoints of unsupported metric types that are not stored", "{datapoint}")
	catalogCacheLookups  = int64Counter("catalog.cache.lookups", "Catalog cache lookups by result (hit, miss)", "{lookup}")
	catalogCacheResets   = int64Counter("catalog.cache.resets", "Times the catalog cache was cleared because it was full", "{reset}")
	clickhouseInsertRows = int64Counter("clickhouse.insert.rows", "Rows written to ClickHouse, by table", "{row}")

	clickhouseInsertDuration = float64Histogram("clickhouse.insert.duration", "ClickHouse insert duration, including the async insert flush, by table and outcome", "s",
		0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10)
)

func RecordExportRequest(ctx context.Context, outcome Outcome) {
	exportRequests.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", string(outcome))))
}

func RecordDatapointsReceived(ctx context.Context, metricType string, count int64) {
	datapointsReceived.Add(ctx, count, metric.WithAttributes(attribute.String("metric.type", metricType)))
}

func RecordDatapointsDropped(ctx context.Context, metricType string, count int64) {
	datapointsDropped.Add(ctx, count, metric.WithAttributes(attribute.String("metric.type", metricType)))
}

func RecordCatalogCacheLookups(ctx context.Context, hits int, misses int) {
	catalogCacheLookups.Add(ctx, int64(hits), metric.WithAttributes(attribute.String("result", "hit")))
	catalogCacheLookups.Add(ctx, int64(misses), metric.WithAttributes(attribute.String("result", "miss")))
}

func RecordCatalogCacheReset(ctx context.Context) {
	catalogCacheResets.Add(ctx, 1)
}

// RecordClickHouseInsert records the duration and rows of one insert, and logs
// it at Error level when it failed.
func RecordClickHouseInsert(ctx context.Context, table string, rowCount int, duration time.Duration, err error) {
	outcome := OutcomeSuccess
	if err != nil {
		outcome = OutcomeFailure
		slog.ErrorContext(ctx, "ClickHouse insert failed",
			slog.String("table", table), slog.Int("rows", rowCount), slog.String("error", err.Error()))
	} else {
		clickhouseInsertRows.Add(ctx, int64(rowCount), metric.WithAttributes(attribute.String("table", table)))
	}
	clickhouseInsertDuration.Record(ctx, duration.Seconds(),
		metric.WithAttributes(attribute.String("table", table), attribute.String("outcome", string(outcome))))
}

func int64Counter(metricName, description, unit string) metric.Int64Counter {
	counter, err := meter.Int64Counter(metricPrefix+metricName, metric.WithDescription(description), metric.WithUnit(unit))
	if err != nil {
		panic(err)
	}
	return counter
}

func float64Histogram(metricName, description, unit string, bucketBoundaries ...float64) metric.Float64Histogram {
	histogram, err := meter.Float64Histogram(metricPrefix+metricName, metric.WithDescription(description), metric.WithUnit(unit),
		metric.WithExplicitBucketBoundaries(bucketBoundaries...))
	if err != nil {
		panic(err)
	}
	return histogram
}
