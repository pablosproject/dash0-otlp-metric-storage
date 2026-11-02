package metrics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"dash0.com/otlp-log-processor-backend/internal/telemetry"
)

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
	rows := mapExportRequest(request)
	telemetry.RecordDatapointsReceived(ctx, "gauge", int64(len(rows.gauges)))
	telemetry.RecordDatapointsReceived(ctx, "sum", int64(len(rows.sums)))
	for metricType, count := range rows.rejectedDatapoints {
		telemetry.RecordDatapointsReceived(ctx, metricType, count)
		telemetry.RecordDatapointsDropped(ctx, metricType, count)
	}

	if err := m.writeRows(ctx, rows); err != nil {
		telemetry.RecordExportRequest(ctx, telemetry.OutcomeFailure)
		return nil, exportError(ctx, err)
	}

	response := &colmetricspb.ExportMetricsServiceResponse{PartialSuccess: partialSuccess(rows.rejectedDatapoints)}
	if response.PartialSuccess != nil {
		telemetry.RecordExportRequest(ctx, telemetry.OutcomePartialSuccess)
		slog.WarnContext(ctx, "Rejected datapoints of unsupported metric types",
			slog.Int64("rejectedDatapoints", response.PartialSuccess.RejectedDataPoints),
			slog.String("detail", response.PartialSuccess.ErrorMessage))
	} else {
		telemetry.RecordExportRequest(ctx, telemetry.OutcomeSuccess)
	}
	return response, nil
}

func (m *dash0MetricsServiceServer) writeRows(ctx context.Context, rows metricRows) error {
	var newCatalogRows []CatalogRow
	for _, row := range rows.catalog {
		if !m.catalogCache.has(row.catalogKey()) {
			newCatalogRows = append(newCatalogRows, row)
		}
	}
	telemetry.RecordCatalogCacheLookups(ctx, len(rows.catalog)-len(newCatalogRows), len(newCatalogRows))
	if err := m.store.InsertCatalogRows(ctx, newCatalogRows); err != nil {
		return fmt.Errorf("inserting catalog rows: %w", err)
	}
	for _, row := range newCatalogRows {
		if m.catalogCache.add(row.catalogKey()) {
			telemetry.RecordCatalogCacheReset(ctx)
		}
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
