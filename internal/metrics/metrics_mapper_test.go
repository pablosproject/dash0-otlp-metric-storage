package metrics

import (
	"testing"
	"time"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

func strVal(s string) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: s}}
}

func intVal(i int64) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: i}}
}

func kv(k string, v *commonpb.AnyValue) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: v}
}

func TestAnyValueToString(t *testing.T) {
	tests := map[string]struct {
		in   *commonpb.AnyValue
		want string
	}{
		"nil value": {in: nil, want: ""},
		"string":    {in: strVal("abc"), want: "abc"},
		"int":       {in: intVal(123), want: "123"},
		"double":    {in: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: 1.5}}, want: "1.5"},
		"bool":      {in: &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: true}}, want: "true"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := anyValueToString(tt.in); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestKvToMap_SameTextDifferentTypeIsSameSeries(t *testing.T) {
	a, b := baseSeriesRow(), baseSeriesRow()
	a.DataPointAttributes = kvToMap([]*commonpb.KeyValue{kv("cpu", intVal(1))})
	b.DataPointAttributes = kvToMap([]*commonpb.KeyValue{kv("cpu", strVal("1"))})
	if metricHash(&a) != metricHash(&b) {
		t.Errorf("expected int 1 and string \"1\" to give the same MetricHash")
	}
}

func numberDataPoint(t time.Time, value float64, attrs ...*commonpb.KeyValue) *metricspb.NumberDataPoint {
	return &metricspb.NumberDataPoint{
		Attributes:        attrs,
		StartTimeUnixNano: uint64(t.Add(-time.Minute).UnixNano()),
		TimeUnixNano:      uint64(t.UnixNano()),
		Value:             &metricspb.NumberDataPoint_AsDouble{AsDouble: value},
	}
}

func gaugeMetric(name string, dps ...*metricspb.NumberDataPoint) *metricspb.Metric {
	return &metricspb.Metric{
		Name: name,
		Unit: "1",
		Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: dps}},
	}
}

func sumMetric(name string, dps ...*metricspb.NumberDataPoint) *metricspb.Metric {
	return &metricspb.Metric{
		Name: name,
		Unit: "{request}",
		Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
			AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
			IsMonotonic:            true,
			DataPoints:             dps,
		}},
	}
}

func scopeMetrics(scopeName string, metrics ...*metricspb.Metric) *metricspb.ScopeMetrics {
	return &metricspb.ScopeMetrics{
		Scope:   &commonpb.InstrumentationScope{Name: scopeName},
		Metrics: metrics,
	}
}

func resourceMetrics(serviceName string, sms ...*metricspb.ScopeMetrics) *metricspb.ResourceMetrics {
	return &metricspb.ResourceMetrics{
		Resource: &resourcepb.Resource{
			Attributes: []*commonpb.KeyValue{kv("service.name", strVal(serviceName))},
		},
		ScopeMetrics: sms,
	}
}

func exportRequest(resourceMetrics ...*metricspb.ResourceMetrics) *colmetricspb.ExportMetricsServiceRequest {
	return &colmetricspb.ExportMetricsServiceRequest{ResourceMetrics: resourceMetrics}
}

func TestMapExportRequest_GaugeAndSum(t *testing.T) {
	ts := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	rows := mapExportRequest(exportRequest(
		resourceMetrics("api", scopeMetrics("lib",
			gaugeMetric("cpu.utilization", numberDataPoint(ts, 0.5, kv("cpu", intVal(0)))),
			sumMetric("http.requests", numberDataPoint(ts, 42, kv("method", strVal("GET")))),
		)),
	))

	if len(rows.series) != 2 || len(rows.gauges) != 1 || len(rows.sums) != 1 {
		t.Fatalf("got %d series, %d gauges, %d sums; want 2, 1, 1", len(rows.series), len(rows.gauges), len(rows.sums))
	}

	gaugeSeries, sumSeries := rows.series[0], rows.series[1]
	wantDay := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)

	if gaugeSeries.MetricType != MetricTypeGauge || gaugeSeries.AggregationTemporality != 0 || gaugeSeries.IsMonotonic {
		t.Errorf("unexpected gauge series type fields: %+v", gaugeSeries)
	}
	if sumSeries.MetricType != MetricTypeSum || sumSeries.AggregationTemporality != 2 || !sumSeries.IsMonotonic {
		t.Errorf("unexpected sum series type fields: %+v", sumSeries)
	}
	for _, s := range rows.series {
		if !s.Day.Equal(wantDay) {
			t.Errorf("series %s: got Day %v, want %v", s.MetricName, s.Day, wantDay)
		}
		if s.ServiceName != "api" || s.ScopeName != "lib" {
			t.Errorf("series %s: got ServiceName %q, ScopeName %q", s.MetricName, s.ServiceName, s.ScopeName)
		}
		if s.MetricHash != metricHash(&s) {
			t.Errorf("series %s: MetricHash does not match the row content", s.MetricName)
		}
	}
	if gaugeSeries.DataPointAttributes["cpu"] != "0" {
		t.Errorf("got DataPointAttributes %v, want cpu=0", gaugeSeries.DataPointAttributes)
	}

	gauge, sum := rows.gauges[0], rows.sums[0]
	if gauge.MetricHash != gaugeSeries.MetricHash || sum.MetricHash != sumSeries.MetricHash {
		t.Errorf("datapoints do not reference their catalog rows")
	}
	if gauge.Value != 0.5 || sum.Value != 42 {
		t.Errorf("got values %v and %v, want 0.5 and 42", gauge.Value, sum.Value)
	}
	if !gauge.TimeUnix.Equal(ts) || !gauge.StartTimeUnix.Equal(ts.Add(-time.Minute)) {
		t.Errorf("got TimeUnix %v, StartTimeUnix %v", gauge.TimeUnix, gauge.StartTimeUnix)
	}
}

func TestMapExportRequest_CatalogRows(t *testing.T) {
	midday := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	beforeMidnight := time.Date(2026, 3, 10, 23, 59, 59, 0, time.UTC)
	afterMidnight := time.Date(2026, 3, 11, 0, 0, 1, 0, time.UTC)

	tests := map[string]struct {
		in         []*metricspb.ResourceMetrics
		wantSeries int
		wantGauges int
	}{
		"same series on the same day gives one catalog row": {
			in: []*metricspb.ResourceMetrics{resourceMetrics("api", scopeMetrics("lib",
				gaugeMetric("cpu", numberDataPoint(midday, 1), numberDataPoint(midday.Add(time.Second), 2), numberDataPoint(beforeMidnight, 3)),
			))},
			wantSeries: 1,
			wantGauges: 3,
		},
		"datapoints across UTC midnight give one catalog row per day": {
			in: []*metricspb.ResourceMetrics{resourceMetrics("api", scopeMetrics("lib",
				gaugeMetric("cpu", numberDataPoint(beforeMidnight, 1), numberDataPoint(afterMidnight, 2)),
			))},
			wantSeries: 2,
			wantGauges: 2,
		},
		"different datapoint attributes give different series": {
			in: []*metricspb.ResourceMetrics{resourceMetrics("api", scopeMetrics("lib",
				gaugeMetric("cpu", numberDataPoint(midday, 1, kv("cpu", intVal(0))), numberDataPoint(midday, 2, kv("cpu", intVal(1)))),
			))},
			wantSeries: 2,
			wantGauges: 2,
		},
		"different resources give different series": {
			in: []*metricspb.ResourceMetrics{
				resourceMetrics("api", scopeMetrics("lib", gaugeMetric("cpu", numberDataPoint(midday, 1)))),
				resourceMetrics("worker", scopeMetrics("lib", gaugeMetric("cpu", numberDataPoint(midday, 1)))),
			},
			wantSeries: 2,
			wantGauges: 2,
		},
		"unsupported metric types are skipped": {
			in: []*metricspb.ResourceMetrics{resourceMetrics("api", scopeMetrics("lib",
				&metricspb.Metric{Name: "latency", Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{
					DataPoints: []*metricspb.HistogramDataPoint{{TimeUnixNano: uint64(midday.UnixNano())}},
				}}},
			))},
			wantSeries: 0,
			wantGauges: 0,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rows := mapExportRequest(exportRequest(tt.in...))
			if len(rows.series) != tt.wantSeries || len(rows.gauges) != tt.wantGauges {
				t.Errorf("got %d series and %d gauges, want %d and %d", len(rows.series), len(rows.gauges), tt.wantSeries, tt.wantGauges)
			}
		})
	}
}

func TestMapExportRequest_LastCatalogRowWins(t *testing.T) {
	ts := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	first := gaugeMetric("cpu", numberDataPoint(ts, 1))
	first.Description = "old"
	second := gaugeMetric("cpu", numberDataPoint(ts, 2))
	second.Description = "new"

	rows := mapExportRequest(exportRequest(
		resourceMetrics("api", scopeMetrics("lib-a", first), scopeMetrics("lib-b", second)),
	))

	if len(rows.series) != 1 {
		t.Fatalf("got %d catalog rows, want 1", len(rows.series))
	}
	if got := rows.series[0]; got.ScopeName != "lib-b" || got.MetricDescription != "new" {
		t.Errorf("got ScopeName %q, MetricDescription %q; want lib-b, new", got.ScopeName, got.MetricDescription)
	}
	if len(rows.gauges) != 2 {
		t.Errorf("got %d gauges, want 2", len(rows.gauges))
	}
}
