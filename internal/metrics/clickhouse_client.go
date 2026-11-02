package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// MetricType matches the Enum8 values of otel_metrics_series.MetricType.
type MetricType int8

const (
	MetricTypeGauge MetricType = 1
	MetricTypeSum   MetricType = 2
)

// SeriesRow is one catalog row of otel_metrics_series: the metadata of a
// series on a given Day.
type SeriesRow struct {
	Day                    time.Time
	MetricHash             uint64
	ServiceName            string
	MetricName             string
	MetricType             MetricType
	MetricUnit             string
	MetricDescription      string
	AggregationTemporality int32
	IsMonotonic            bool
	ResourceAttributes     map[string]string
	ResourceSchemaUrl      string
	ScopeName              string
	ScopeVersion           string
	ScopeAttributes        map[string]string
	ScopeSchemaUrl         string
	ScopeDroppedAttrCount  uint32
	DataPointAttributes    map[string]string
}

// DatapointRow is one row of otel_metrics_gauge or otel_metrics_sum. The
// series metadata lives in the catalog, referenced by MetricHash.
type DatapointRow struct {
	TimeUnix      time.Time
	MetricHash    uint64
	StartTimeUnix time.Time
	Value         float64
	Flags         uint32
}

// MetricsStore is the storage used by the Export write path.
type MetricsStore interface {
	Write(ctx context.Context, rows metricRows) error
}

// ClickHouseMetricsStore implements MetricsStore using a ClickHouse connection.
type ClickHouseMetricsStore struct {
	conn driver.Conn
}

// NewClickHouseMetricsStore creates a new ClickHouseMetricsStore connected to the given address.
func NewClickHouseMetricsStore(ctx context.Context, addr string, database string, username string, password string) (*ClickHouseMetricsStore, error) {
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{addr},
		Auth: clickhouse.Auth{
			Database: database,
			Username: username,
			Password: password,
		},
		Settings: clickhouse.Settings{
			"max_execution_time": 60,
		},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("opening clickhouse connection: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("pinging clickhouse: %w", err)
	}
	return &ClickHouseMetricsStore{conn: conn}, nil
}

// CreateTables executes DDL for the catalog and the datapoint tables.
func (s *ClickHouseMetricsStore) CreateTables(ctx context.Context) error {
	ddls := []string{
		createSeriesTableSQL,
		createGaugeTableSQL,
		createSumTableSQL,
	}
	for _, ddl := range ddls {
		if err := s.conn.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("creating table: %w", err)
		}
	}
	return nil
}

// Write stores the rows of one Export request. Catalog rows are inserted
// first, so a datapoint never references a series missing from the catalog.
// On the first failure nothing else is inserted.
func (s *ClickHouseMetricsStore) Write(ctx context.Context, rows metricRows) error {
	if len(rows.series) > 0 {
		if err := s.insertSeries(ctx, rows.series); err != nil {
			return err
		}
	}
	if len(rows.gauges) > 0 {
		if err := s.insertDatapoints(ctx, "otel_metrics_gauge", rows.gauges); err != nil {
			return err
		}
	}
	if len(rows.sums) > 0 {
		if err := s.insertDatapoints(ctx, "otel_metrics_sum", rows.sums); err != nil {
			return err
		}
	}
	return nil
}

func (s *ClickHouseMetricsStore) insertSeries(ctx context.Context, rows []SeriesRow) error {
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO otel_metrics_series")
	if err != nil {
		return fmt.Errorf("preparing series batch: %w", err)
	}
	for _, row := range rows {
		if err := batch.Append(
			row.Day,
			row.MetricHash,
			row.ServiceName,
			row.MetricName,
			int8(row.MetricType),
			row.MetricUnit,
			row.MetricDescription,
			row.AggregationTemporality,
			row.IsMonotonic,
			row.ResourceAttributes,
			row.ResourceSchemaUrl,
			row.ScopeName,
			row.ScopeVersion,
			row.ScopeAttributes,
			row.ScopeSchemaUrl,
			row.ScopeDroppedAttrCount,
			row.DataPointAttributes,
		); err != nil {
			return fmt.Errorf("appending series row: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("sending series batch: %w", err)
	}
	return nil
}

func (s *ClickHouseMetricsStore) insertDatapoints(ctx context.Context, table string, rows []DatapointRow) error {
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO "+table)
	if err != nil {
		return fmt.Errorf("preparing %s batch: %w", table, err)
	}
	for _, row := range rows {
		if err := batch.Append(
			row.TimeUnix,
			row.MetricHash,
			row.StartTimeUnix,
			row.Value,
			row.Flags,
		); err != nil {
			return fmt.Errorf("appending %s row: %w", table, err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("sending %s batch: %w", table, err)
	}
	return nil
}

// Close closes the underlying ClickHouse connection.
func (s *ClickHouseMetricsStore) Close() error {
	return s.conn.Close()
}
