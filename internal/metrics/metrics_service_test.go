package metrics

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"slices"
	"testing"
	"time"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestExport_StatusAndPartialSuccess(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	supportedOnly := exportRequest(resourceMetrics("api", scopeMetrics("lib",
		gaugeMetric("cpu.utilization", numberDataPoint(now, 0.5)),
	)))
	withUnsupported := exportRequest(resourceMetrics("api", scopeMetrics("lib",
		gaugeMetric("cpu.utilization", numberDataPoint(now, 0.5)),
		histogramMetric("http.server.request.duration", 3),
		summaryMetric("rpc.duration", 1),
	)))

	tests := map[string]struct {
		request          *colmetricspb.ExportMetricsServiceRequest
		storeErr         error
		wantCode         codes.Code
		wantRejected     int64
		wantErrorMessage string
	}{
		"supported metrics only": {
			request:  supportedOnly,
			wantCode: codes.OK,
		},
		"empty request": {
			request:  exportRequest(),
			wantCode: codes.OK,
		},
		"unsupported metric types give partial success": {
			request:          withUnsupported,
			wantCode:         codes.OK,
			wantRejected:     4,
			wantErrorMessage: "unsupported metric types (only Gauge and Sum are stored): histogram=3, summary=1",
		},
		"clickhouse error is retryable": {
			request:  supportedOnly,
			storeErr: errors.New("connection refused"),
			wantCode: codes.Unavailable,
		},
		"deadline exceeded": {
			request:  supportedOnly,
			storeErr: fmt.Errorf("sending batch: %w", context.DeadlineExceeded),
			wantCode: codes.DeadlineExceeded,
		},
		"canceled": {
			request:  supportedOnly,
			storeErr: fmt.Errorf("sending batch: %w", context.Canceled),
			wantCode: codes.Canceled,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			client, closer := server(&fakeStore{catalogErr: tt.storeErr})
			defer closer()

			response, err := client.Export(context.Background(), tt.request)
			if got := status.Code(err); got != tt.wantCode {
				t.Fatalf("got code %v (%v), want %v", got, err, tt.wantCode)
			}
			partialSuccess := response.GetPartialSuccess()
			if partialSuccess.GetRejectedDataPoints() != tt.wantRejected || partialSuccess.GetErrorMessage() != tt.wantErrorMessage {
				t.Errorf("got partial success %v, want rejected %d and message %q", partialSuccess, tt.wantRejected, tt.wantErrorMessage)
			}
		})
	}
}

func server(store MetricsStore) (colmetricspb.MetricsServiceClient, func()) {
	addr := "localhost:4317"
	buffer := 101024 * 1024
	lis := bufconn.Listen(buffer)

	baseServer := grpc.NewServer()
	colmetricspb.RegisterMetricsServiceServer(baseServer, NewServer(addr, store))
	go func() {
		if err := baseServer.Serve(lis); err != nil {
			log.Printf("error serving server: %v", err)
		}
	}()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("error connecting to server: %v", err)
	}

	closer := func() {
		err := lis.Close()
		if err != nil {
			log.Printf("error closing listener: %v", err)
		}
		baseServer.Stop()
	}

	client := colmetricspb.NewMetricsServiceClient(conn)

	return client, closer
}

// fakeStore records the inserts it receives, e.g. "catalog:2", "datapoints:1+1".
type fakeStore struct {
	calls         []string
	catalogErr    error
	datapointsErr error
}

func (s *fakeStore) InsertCatalogRows(_ context.Context, rows []CatalogRow) error {
	s.calls = append(s.calls, fmt.Sprintf("catalog:%d", len(rows)))
	return s.catalogErr
}

func (s *fakeStore) InsertDatapoints(_ context.Context, gauges []DatapointRow, sums []DatapointRow) error {
	s.calls = append(s.calls, fmt.Sprintf("datapoints:%d+%d", len(gauges), len(sums)))
	return s.datapointsErr
}

func TestExport_WritePath(t *testing.T) {
	errClickHouse := errors.New("clickhouse unavailable")

	type exportStep struct {
		catalogErr    error
		datapointsErr error
		wantErr       bool
		wantCalls     []string
	}

	tests := map[string][]exportStep{
		"catalog is written before datapoints": {
			{wantCalls: []string{"catalog:2", "datapoints:1+1"}},
		},
		"known series are not rewritten to the catalog": {
			{wantCalls: []string{"catalog:2", "datapoints:1+1"}},
			{wantCalls: []string{"catalog:0", "datapoints:1+1"}},
		},
		"catalog failure inserts no datapoints and does not cache the series": {
			{catalogErr: errClickHouse, wantErr: true, wantCalls: []string{"catalog:2"}},
			{wantCalls: []string{"catalog:2", "datapoints:1+1"}},
		},
		"datapoint failure keeps the series cached": {
			{datapointsErr: errClickHouse, wantErr: true, wantCalls: []string{"catalog:2", "datapoints:1+1"}},
			{wantCalls: []string{"catalog:0", "datapoints:1+1"}},
		},
	}

	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	request := exportRequest(resourceMetrics("api", scopeMetrics("lib",
		gaugeMetric("cpu.utilization", numberDataPoint(now, 0.5)),
		sumMetric("http.requests", numberDataPoint(now, 7)),
	)))

	for name, steps := range tests {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{}
			server := NewServer("test", store)

			for i, step := range steps {
				store.calls = nil
				store.catalogErr = step.catalogErr
				store.datapointsErr = step.datapointsErr

				_, err := server.Export(context.Background(), request)
				if (err != nil) != step.wantErr {
					t.Fatalf("export %d: got error %v, want error: %t", i, err, step.wantErr)
				}
				if !slices.Equal(store.calls, step.wantCalls) {
					t.Errorf("export %d: got calls %v, want %v", i, store.calls, step.wantCalls)
				}
			}
		})
	}
}
