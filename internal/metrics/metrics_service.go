package metrics

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
)

const name = "dash0.com/otlp-log-processor-backend"

var (
	meter                  = otel.Meter(name)
	metricsReceivedCounter metric.Int64Counter
)

func init() {
	var err error
	metricsReceivedCounter, err = meter.Int64Counter("com.dash0.homeexercise.metrics.received",
		metric.WithDescription("The number of metrics received by otlp-metrics-processor-backend"),
		metric.WithUnit("{metric}"))
	if err != nil {
		panic(err)
	}
}

// defaultCatalogCacheMaxSize bounds the catalog cache to a few MB of keys.
const defaultCatalogCacheMaxSize = 100_000

type dash0MetricsServiceServer struct {
	addr         string
	store        MetricsStore
	catalogCache *catalogCache

	colmetricspb.UnimplementedMetricsServiceServer
}

func NewServer(addr string, store MetricsStore) colmetricspb.MetricsServiceServer {
	return &dash0MetricsServiceServer{
		addr:         addr,
		store:        store,
		catalogCache: newCatalogCache(defaultCatalogCacheMaxSize),
	}
}

func (m *dash0MetricsServiceServer) Export(ctx context.Context, request *colmetricspb.ExportMetricsServiceRequest) (*colmetricspb.ExportMetricsServiceResponse, error) {
	slog.DebugContext(ctx, "Received ExportMetricsServiceRequest")
	metricsReceivedCounter.Add(ctx, 1)

	if m.store != nil {
		if err := m.writeRows(ctx, mapExportRequest(request)); err != nil {
			return nil, fmt.Errorf("writing metrics: %w", err)
		}
	}

	return &colmetricspb.ExportMetricsServiceResponse{}, nil
}

// writeRows inserts catalog rows before the datapoints that reference them,
// so a datapoint never references a series missing from the catalog. If the
// catalog insert fails, no datapoints are inserted and the cache is unchanged.
func (m *dash0MetricsServiceServer) writeRows(ctx context.Context, rows metricRows) error {
	var newCatalogRows []CatalogRow
	for _, row := range rows.catalog {
		if !m.catalogCache.has(row.catalogKey()) {
			newCatalogRows = append(newCatalogRows, row)
		}
	}
	if err := m.store.InsertCatalogRows(ctx, newCatalogRows); err != nil {
		return fmt.Errorf("inserting catalog rows: %w", err)
	}
	for _, row := range newCatalogRows {
		m.catalogCache.add(row.catalogKey())
	}
	if err := m.store.InsertDatapoints(ctx, rows.gauges, rows.sums); err != nil {
		return fmt.Errorf("inserting datapoints: %w", err)
	}
	return nil
}
