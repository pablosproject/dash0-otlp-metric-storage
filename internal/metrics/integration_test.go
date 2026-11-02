//go:build integration

package metrics

import (
	"context"
	"fmt"
	"log"
	"net"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func setupClickHouse(t *testing.T) (*ClickHouseMetricsStore, func()) {
	t.Helper()
	ctx := context.Background()

	ctr, err := testcontainers.Run(ctx, "clickhouse/clickhouse-server:26.2",
		testcontainers.WithExposedPorts("9000/tcp"),
		testcontainers.WithEnv(map[string]string{
			"CLICKHOUSE_USER":     "default",
			"CLICKHOUSE_PASSWORD": "test",
		}),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("9000/tcp").WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("starting clickhouse container: %v", err)
	}

	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatalf("getting container host: %v", err)
	}
	mappedPort, err := ctr.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatalf("getting mapped port: %v", err)
	}

	addr := fmt.Sprintf("%s:%s", host, mappedPort.Port())
	store, err := NewClickHouseMetricsStore(ctx, addr, "default", "default", "test")
	if err != nil {
		t.Fatalf("creating clickhouse metrics store: %v", err)
	}

	cleanup := func() {
		store.Close()
		if err := ctr.Terminate(ctx); err != nil {
			t.Logf("terminating clickhouse container: %v", err)
		}
	}

	return store, cleanup
}

func TestCreateTables(t *testing.T) {
	store, cleanup := setupClickHouse(t)
	defer cleanup()

	ctx := context.Background()
	if err := store.CreateTables(ctx); err != nil {
		t.Fatalf("creating tables: %v", err)
	}

	rows, err := store.conn.Query(ctx, "SELECT name FROM system.tables WHERE database = 'default' ORDER BY name")
	if err != nil {
		t.Fatalf("querying system.tables: %v", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scanning table name: %v", err)
		}
		tables = append(tables, name)
	}

	want := []string{"otel_metrics_gauge", "otel_metrics_series", "otel_metrics_sum"}
	if !slices.Equal(tables, want) {
		t.Errorf("got tables %v, want %v", tables, want)
	}
}

func TestInsertSeries(t *testing.T) {
	store, cleanup := setupClickHouse(t)
	defer cleanup()

	ctx := context.Background()
	if err := store.CreateTables(ctx); err != nil {
		t.Fatalf("creating tables: %v", err)
	}

	want := SeriesRow{
		Day:                    time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC),
		MetricHash:             12345,
		ServiceName:            "api",
		MetricName:             "http.requests",
		MetricType:             MetricTypeSum,
		MetricUnit:             "{request}",
		MetricDescription:      "Total HTTP requests",
		AggregationTemporality: 2,
		IsMonotonic:            true,
		ResourceAttributes:     map[string]string{"service.name": "api", "host.name": "web-1"},
		ResourceSchemaUrl:      "https://opentelemetry.io/schemas/1.4.0",
		ScopeName:              "lib",
		ScopeVersion:           "1.0.0",
		ScopeAttributes:        map[string]string{"k": "v"},
		ScopeSchemaUrl:         "https://example.com/scope",
		ScopeDroppedAttrCount:  3,
		DataPointAttributes:    map[string]string{"method": "GET"},
	}
	if err := store.insertSeries(ctx, []SeriesRow{want}); err != nil {
		t.Fatalf("inserting series rows: %v", err)
	}

	var (
		got        SeriesRow
		metricType string
	)
	err := store.conn.QueryRow(ctx, `
		SELECT Day, MetricHash, ServiceName, MetricName, MetricType, MetricUnit, MetricDescription,
		       AggregationTemporality, IsMonotonic, ResourceAttributes, ResourceSchemaUrl,
		       ScopeName, ScopeVersion, ScopeAttributes, ScopeSchemaUrl, ScopeDroppedAttrCount,
		       DataPointAttributes
		FROM otel_metrics_series`,
	).Scan(&got.Day, &got.MetricHash, &got.ServiceName, &got.MetricName, &metricType, &got.MetricUnit,
		&got.MetricDescription, &got.AggregationTemporality, &got.IsMonotonic, &got.ResourceAttributes,
		&got.ResourceSchemaUrl, &got.ScopeName, &got.ScopeVersion, &got.ScopeAttributes, &got.ScopeSchemaUrl,
		&got.ScopeDroppedAttrCount, &got.DataPointAttributes)
	if err != nil {
		t.Fatalf("querying series: %v", err)
	}

	if metricType != "sum" {
		t.Errorf("got MetricType %q, want sum", metricType)
	}
	got.MetricType = MetricTypeSum
	if !got.Day.Equal(want.Day) {
		t.Errorf("got Day %v, want %v", got.Day, want.Day)
	}
	got.Day = want.Day
	if !reflect.DeepEqual(got, want) {
		t.Errorf("series row mismatch:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestInsertDatapoints(t *testing.T) {
	store, cleanup := setupClickHouse(t)
	defer cleanup()

	ctx := context.Background()
	if err := store.CreateTables(ctx); err != nil {
		t.Fatalf("creating tables: %v", err)
	}

	ts := time.Date(2026, 3, 10, 12, 0, 0, 123456789, time.UTC)
	want := DatapointRow{
		TimeUnix:      ts,
		MetricHash:    12345,
		StartTimeUnix: ts.Add(-time.Minute),
		Value:         42.5,
		Flags:         1,
	}

	tables := []string{"otel_metrics_gauge", "otel_metrics_sum"}

	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			if err := store.insertDatapoints(ctx, table, []DatapointRow{want}); err != nil {
				t.Fatalf("inserting datapoints: %v", err)
			}

			var got DatapointRow
			err := store.conn.QueryRow(ctx,
				"SELECT TimeUnix, MetricHash, StartTimeUnix, Value, Flags FROM "+table,
			).Scan(&got.TimeUnix, &got.MetricHash, &got.StartTimeUnix, &got.Value, &got.Flags)
			if err != nil {
				t.Fatalf("querying %s: %v", table, err)
			}

			if !got.TimeUnix.Equal(want.TimeUnix) || !got.StartTimeUnix.Equal(want.StartTimeUnix) {
				t.Errorf("got TimeUnix %v, StartTimeUnix %v; want %v, %v", got.TimeUnix, got.StartTimeUnix, want.TimeUnix, want.StartTimeUnix)
			}
			if got.MetricHash != want.MetricHash || got.Value != want.Value || got.Flags != want.Flags {
				t.Errorf("got %+v, want %+v", got, want)
			}
		})
	}
}

func TestGRPCToClickHouse(t *testing.T) {
	store, cleanup := setupClickHouse(t)
	defer cleanup()

	ctx := context.Background()
	if err := store.CreateTables(ctx); err != nil {
		t.Fatalf("creating tables: %v", err)
	}

	lis := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	colmetricspb.RegisterMetricsServiceServer(grpcServer, NewServer("bufconn", store))
	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.Printf("error serving server: %v", err)
		}
	}()
	defer grpcServer.Stop()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("connecting to grpc server: %v", err)
	}
	defer conn.Close()

	client := colmetricspb.NewMetricsServiceClient(conn)

	now := time.Now().UTC()
	_, err = client.Export(ctx, &colmetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{
			resourceMetrics("e2e-service", scopeMetrics("e2e-scope",
				gaugeMetric("e2e.gauge", numberDataPoint(now, 99.9), numberDataPoint(now.Add(time.Second), 100.1)),
				sumMetric("e2e.sum", numberDataPoint(now, 7)),
			)),
		},
	})
	if err != nil {
		t.Fatalf("exporting metrics via grpc: %v", err)
	}

	var catalogRows uint64
	if err := store.conn.QueryRow(ctx, "SELECT count() FROM otel_metrics_series").Scan(&catalogRows); err != nil {
		t.Fatalf("counting catalog rows: %v", err)
	}
	if catalogRows != 2 {
		t.Errorf("got %d catalog rows, want 2", catalogRows)
	}

	tests := map[string]struct {
		table      string
		metricName string
		wantValues []float64
	}{
		"gauge": {table: "otel_metrics_gauge", metricName: "e2e.gauge", wantValues: []float64{99.9, 100.1}},
		"sum":   {table: "otel_metrics_sum", metricName: "e2e.sum", wantValues: []float64{7}},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			query := fmt.Sprintf(`
				SELECT s.ServiceName, s.MetricName, d.Value
				FROM %s AS d
				INNER JOIN (
					SELECT MetricHash, ServiceName, MetricName
					FROM otel_metrics_series
					WHERE Day BETWEEN toDate($1) AND toDate($2)
					ORDER BY Day DESC
					LIMIT 1 BY MetricHash
				) AS s USING MetricHash
				WHERE d.TimeUnix BETWEEN $1 AND $2
				ORDER BY d.TimeUnix`, tt.table)

			rows, err := store.conn.Query(ctx, query, now.Add(-time.Minute), now.Add(time.Minute))
			if err != nil {
				t.Fatalf("querying %s joined with the catalog: %v", tt.table, err)
			}
			defer rows.Close()

			var values []float64
			for rows.Next() {
				var svcName, metricName string
				var value float64
				if err := rows.Scan(&svcName, &metricName, &value); err != nil {
					t.Fatalf("scanning row: %v", err)
				}
				if svcName != "e2e-service" || metricName != tt.metricName {
					t.Errorf("got ServiceName %q, MetricName %q; want e2e-service, %s", svcName, metricName, tt.metricName)
				}
				values = append(values, value)
			}
			if !slices.Equal(values, tt.wantValues) {
				t.Errorf("got values %v, want %v", values, tt.wantValues)
			}
		})
	}
}
