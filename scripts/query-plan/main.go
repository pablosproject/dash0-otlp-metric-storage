// Command query-plan loads synthetic metrics into a scratch database of the
// local ClickHouse, using the service's own DDL, and shows for two
// production-like read queries how many parts and granules each table read
// selects (EXPLAIN indexes = 1) and how much data the execution actually read
// (system.query_log).
//
// Usage: docker compose up -d clickhouse && go run ./scripts/query-plan
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"dash0.com/otlp-log-processor-backend/internal/metrics"
)

const database = "query_plan"

const seriesSQL = `
SELECT
    'service-' || toString(number % 10)             AS ServiceName,
    'metric.' || toString(intDiv(number, 10) % 20)  AS MetricName,
    'host-' || toString(intDiv(number, 200))        AS Host,
    cityHash64(ServiceName, MetricName, Host)       AS MetricHash
FROM numbers(2000)`

var seedSteps = []struct {
	description string
	sql         string
}{
	{
		description: "catalog: 2000 series per day, 2026-02-01 to 2026-03-31",
		sql: `
INSERT INTO otel_metrics_catalog (Day, MetricHash, ServiceName, MetricName, MetricType, ResourceAttributes, DataPointAttributes)
SELECT toDate('2026-02-01') + days.number, MetricHash, ServiceName, MetricName, 'gauge',
       map('service.name', ServiceName), map('host', Host)
FROM (` + seriesSQL + `) AS series
CROSS JOIN numbers(59) AS days`,
	},
	{
		description: "gauge: 2000 series every 30s, 2026-03-09 to 2026-03-11",
		sql: `
INSERT INTO otel_metrics_gauge (TimeUnix, MetricHash, StartTimeUnix, Value, Flags)
SELECT toDateTime64('2026-03-09 00:00:00', 9, 'UTC') + toIntervalSecond(ticks.number * 30), MetricHash,
       toDateTime64('2026-03-09 00:00:00', 9, 'UTC'), rand() % 100, 0
FROM (` + seriesSQL + `) AS series
CROSS JOIN numbers(3 * 2880) AS ticks`,
	},
	{description: "merging catalog parts", sql: "OPTIMIZE TABLE otel_metrics_catalog FINAL"},
	{description: "merging gauge parts", sql: "OPTIMIZE TABLE otel_metrics_gauge FINAL"},
}

var targetQueries = []struct {
	name string
	sql  string
}{
	{
		name: "Chart: one metric across all services, last hour, per-minute average",
		sql: `
WITH series AS (
    SELECT MetricHash, ServiceName
    FROM otel_metrics_catalog
    WHERE Day BETWEEN toDate('2026-03-10') AND toDate('2026-03-10')
      AND MetricName = 'metric.7'
    ORDER BY Day DESC
    LIMIT 1 BY MetricHash
)
SELECT toStartOfMinute(d.TimeUnix) AS Minute, s.ServiceName, avg(d.Value) AS AverageValue
FROM otel_metrics_gauge AS d
INNER JOIN series AS s USING MetricHash
WHERE d.MetricHash IN (SELECT MetricHash FROM series)
  AND d.TimeUnix >= toDateTime64('2026-03-10 12:00:00', 9, 'UTC')
  AND d.TimeUnix <  toDateTime64('2026-03-10 13:00:00', 9, 'UTC')
GROUP BY Minute, s.ServiceName
ORDER BY Minute, s.ServiceName`,
	},
	{
		name: "Service overview: latest value of every series of one service, last hour",
		sql: `
WITH series AS (
    SELECT MetricHash, MetricName, DataPointAttributes['host'] AS Host
    FROM otel_metrics_catalog
    WHERE Day BETWEEN toDate('2026-03-10') AND toDate('2026-03-10')
      AND ServiceName = 'service-3'
    ORDER BY Day DESC
    LIMIT 1 BY MetricHash
)
SELECT s.MetricName, s.Host, argMax(d.Value, d.TimeUnix) AS LastValue, max(d.TimeUnix) AS LastSeen
FROM otel_metrics_gauge AS d
INNER JOIN series AS s USING MetricHash
WHERE d.MetricHash IN (SELECT MetricHash FROM series)
  AND d.TimeUnix >= toDateTime64('2026-03-10 12:00:00', 9, 'UTC')
  AND d.TimeUnix <  toDateTime64('2026-03-10 13:00:00', 9, 'UTC')
GROUP BY s.MetricName, s.Host
ORDER BY s.MetricName, s.Host`,
	},
}

type indexStats struct {
	Type             string   `json:"Type"`
	Keys             []string `json:"Keys"`
	InitialParts     int      `json:"Initial Parts"`
	SelectedParts    int      `json:"Selected Parts"`
	InitialGranules  int      `json:"Initial Granules"`
	SelectedGranules int      `json:"Selected Granules"`
}

type explainNode struct {
	NodeType    string        `json:"Node Type"`
	Description string        `json:"Description"`
	Indexes     []indexStats  `json:"Indexes"`
	Plans       []explainNode `json:"Plans"`
}

func main() {
	addr := flag.String("clickhouseAddr", "localhost:9000", "The ClickHouse native protocol address")
	username := flag.String("clickhouseUsername", "default", "The ClickHouse username; the password is read from CLICKHOUSE_PASSWORD")
	flag.Parse()
	password := os.Getenv("CLICKHOUSE_PASSWORD")
	if password == "" {
		password = "dash0"
	}

	if err := run(context.Background(), *addr, *username, password); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, addr string, username string, password string) error {
	if err := recreateDatabase(ctx, addr, username, password); err != nil {
		return err
	}
	if err := createTables(ctx, addr, username, password); err != nil {
		return err
	}

	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr:     []string{addr},
		Auth:     clickhouse.Auth{Database: database, Username: username, Password: password},
		Settings: clickhouse.Settings{"max_execution_time": 300},
	})
	if err != nil {
		return fmt.Errorf("opening clickhouse connection: %w", err)
	}
	defer conn.Close()

	for _, step := range seedSteps {
		fmt.Printf("==> Seeding %s\n", step.description)
		if err := conn.Exec(ctx, step.sql); err != nil {
			return fmt.Errorf("seeding %s: %w", step.description, err)
		}
	}
	if err := printTableSizes(ctx, conn); err != nil {
		return err
	}

	for _, target := range targetQueries {
		fmt.Printf("\n==================================================================\n%s\n", target.name)
		fmt.Printf("------------------------------------------------------------------%s\n\n", target.sql)
		if err := printIndexAnalysis(ctx, conn, target.sql); err != nil {
			return err
		}
		if err := printExecutionStats(ctx, conn, target.sql); err != nil {
			return err
		}
	}
	return nil
}

func recreateDatabase(ctx context.Context, addr string, username string, password string) error {
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{addr},
		Auth: clickhouse.Auth{Database: "default", Username: username, Password: password},
	})
	if err != nil {
		return fmt.Errorf("opening clickhouse connection: %w", err)
	}
	defer conn.Close()

	for _, statement := range []string{"DROP DATABASE IF EXISTS " + database, "CREATE DATABASE " + database} {
		if err := conn.Exec(ctx, statement); err != nil {
			return fmt.Errorf("recreating database %s: %w", database, err)
		}
	}
	return nil
}

func createTables(ctx context.Context, addr string, username string, password string) error {
	store, err := metrics.NewClickHouseMetricsStore(ctx, addr, database, username, password)
	if err != nil {
		return err
	}
	defer store.Close()
	return store.CreateTables(ctx)
}

func printTableSizes(ctx context.Context, conn driver.Conn) error {
	rows, err := conn.Query(ctx, `
		SELECT table, count(), sum(rows), sum(marks)
		FROM system.parts
		WHERE database = ? AND active AND rows > 0
		GROUP BY table
		ORDER BY table`, database)
	if err != nil {
		return fmt.Errorf("reading table sizes: %w", err)
	}
	defer rows.Close()

	fmt.Printf("\n%-22s %8s %12s %10s\n", "table", "parts", "rows", "granules")
	for rows.Next() {
		var table string
		var parts, rowCount, granules uint64
		if err := rows.Scan(&table, &parts, &rowCount, &granules); err != nil {
			return fmt.Errorf("scanning table sizes: %w", err)
		}
		fmt.Printf("%-22s %8d %12d %10d\n", table, parts, rowCount, granules)
	}
	return rows.Err()
}

func printIndexAnalysis(ctx context.Context, conn driver.Conn, query string) error {
	rows, err := conn.Query(ctx, "EXPLAIN json = 1, indexes = 1 "+query)
	if err != nil {
		return fmt.Errorf("running explain: %w", err)
	}
	defer rows.Close()

	var output strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Errorf("scanning explain row: %w", err)
		}
		output.WriteString(line)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading explain rows: %w", err)
	}

	var plans []struct {
		Plan explainNode `json:"Plan"`
	}
	if err := json.Unmarshal([]byte(output.String()), &plans); err != nil {
		return fmt.Errorf("parsing explain json: %w", err)
	}

	fmt.Println("EXPLAIN indexes = 1 (each index starts from what the previous one selected):")
	var printReads func(node explainNode)
	printReads = func(node explainNode) {
		if node.NodeType == "ReadFromMergeTree" {
			fmt.Printf("  %s\n", node.Description)
			for _, index := range node.Indexes {
				fmt.Printf("    %-10s parts %4d/%-4d granules %6d/%-6d keys %s\n", index.Type,
					index.SelectedParts, index.InitialParts, index.SelectedGranules, index.InitialGranules,
					strings.Join(index.Keys, ", "))
			}
		}
		for _, child := range node.Plans {
			printReads(child)
		}
	}
	for _, plan := range plans {
		printReads(plan.Plan)
	}
	return nil
}

func printExecutionStats(ctx context.Context, conn driver.Conn, query string) error {
	queryID := fmt.Sprintf("query-plan-%d", time.Now().UnixNano())
	rows, err := conn.Query(clickhouse.Context(ctx, clickhouse.WithQueryID(queryID)), query)
	if err != nil {
		return fmt.Errorf("running query: %w", err)
	}
	resultRows := 0
	for rows.Next() {
		resultRows++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("reading query result: %w", err)
	}
	rows.Close()

	if err := conn.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		return fmt.Errorf("flushing logs: %w", err)
	}
	var readRows, readBytes, durationMs, selectedParts, selectedGranules uint64
	if err := conn.QueryRow(ctx, `
		SELECT read_rows, read_bytes, query_duration_ms,
		       ProfileEvents['SelectedParts'], ProfileEvents['SelectedMarks']
		FROM system.query_log
		WHERE query_id = ? AND type = 'QueryFinish'`, queryID,
	).Scan(&readRows, &readBytes, &durationMs, &selectedParts, &selectedGranules); err != nil {
		return fmt.Errorf("reading query log: %w", err)
	}

	fmt.Println("\nExecution (system.query_log, all tables of the query):")
	fmt.Printf("  result rows %d, read rows %d, read %.1f MiB, selected parts %d, selected granules %d, %d ms\n",
		resultRows, readRows, float64(readBytes)/(1<<20), selectedParts, selectedGranules, durationMs)
	return nil
}
