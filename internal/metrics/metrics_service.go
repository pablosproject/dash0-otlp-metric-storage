package metrics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

	rows := mapExportRequest(request)
	if err := m.writeRows(ctx, rows); err != nil {
		slog.ErrorContext(ctx, "Storing metrics failed", slog.String("error", err.Error()))
		return nil, exportError(ctx, err)
	}

	return &colmetricspb.ExportMetricsServiceResponse{PartialSuccess: partialSuccess(rows.rejectedDatapoints)}, nil
}

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

func exportError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "storing metrics: deadline exceeded")
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		return status.Error(codes.Canceled, "storing metrics: canceled")
	default:
		return status.Error(codes.Unavailable, "storing metrics failed")
	}
}

func partialSuccess(rejectedDatapoints map[string]int64) *colmetricspb.ExportMetricsPartialSuccess {
	if len(rejectedDatapoints) == 0 {
		return nil
	}
	var total int64
	var counts []string
	for _, metricType := range slices.Sorted(maps.Keys(rejectedDatapoints)) {
		total += rejectedDatapoints[metricType]
		counts = append(counts, fmt.Sprintf("%s=%d", metricType, rejectedDatapoints[metricType]))
	}
	return &colmetricspb.ExportMetricsPartialSuccess{
		RejectedDataPoints: total,
		ErrorMessage:       "unsupported metric types (only Gauge and Sum are stored): " + strings.Join(counts, ", "),
	}
}
