package metrics

import (
	"testing"
	"time"
)

func baseSeriesRow() SeriesRow {
	return SeriesRow{
		MetricName:             "http.requests",
		MetricType:             MetricTypeSum,
		MetricUnit:             "{request}",
		AggregationTemporality: 2,
		IsMonotonic:            true,
		ResourceAttributes:     map[string]string{"service.name": "api", "host.name": "web-1"},
		DataPointAttributes:    map[string]string{"method": "GET", "status": "200"},
	}
}

func TestMetricHash_Golden(t *testing.T) {
	r := baseSeriesRow()
	const want uint64 = 13229911287843068379
	if got := metricHash(&r); got != want {
		t.Errorf("metricHash changed: got %d, want %d. A new encoding splits the history of every series.", got, want)
	}
}

func TestMetricHash_SameSeries(t *testing.T) {
	tests := map[string]func(r *SeriesRow){
		"attributes inserted in a different order": func(r *SeriesRow) {
			r.ResourceAttributes = map[string]string{"host.name": "web-1", "service.name": "api"}
		},
		"different scope": func(r *SeriesRow) {
			r.ScopeName, r.ScopeVersion = "other-lib", "2.0.0"
			r.ScopeAttributes = map[string]string{"k": "v"}
		},
		"different description": func(r *SeriesRow) { r.MetricDescription = "changed" },
		"different schema urls": func(r *SeriesRow) {
			r.ResourceSchemaUrl, r.ScopeSchemaUrl = "https://a", "https://b"
		},
		"different service name column": func(r *SeriesRow) { r.ServiceName = "other" },
		"different day":                 func(r *SeriesRow) { r.Day = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) },
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			base := baseSeriesRow()
			other := baseSeriesRow()
			mutate(&other)
			if metricHash(&base) != metricHash(&other) {
				t.Errorf("expected the same MetricHash")
			}
		})
	}
}

func TestMetricHash_NilAndEmptyAttributesAreEqual(t *testing.T) {
	a, b := baseSeriesRow(), baseSeriesRow()
	a.DataPointAttributes = nil
	b.DataPointAttributes = map[string]string{}
	if metricHash(&a) != metricHash(&b) {
		t.Errorf("expected nil and empty DataPointAttributes to give the same MetricHash")
	}
}

func TestMetricHash_DifferentSeries(t *testing.T) {
	tests := map[string]func(r *SeriesRow){
		"different metric name":  func(r *SeriesRow) { r.MetricName = "http.responses" },
		"different unit":         func(r *SeriesRow) { r.MetricUnit = "1" },
		"different type":         func(r *SeriesRow) { r.MetricType = MetricTypeGauge },
		"delta vs cumulative":    func(r *SeriesRow) { r.AggregationTemporality = 1 },
		"different monotonicity": func(r *SeriesRow) { r.IsMonotonic = false },
		"different resource attribute value": func(r *SeriesRow) {
			r.ResourceAttributes = map[string]string{"service.name": "api", "host.name": "web-2"}
		},
		"different datapoint attribute value": func(r *SeriesRow) {
			r.DataPointAttributes = map[string]string{"method": "GET", "status": "500"}
		},
		"extra datapoint attribute": func(r *SeriesRow) {
			r.DataPointAttributes = map[string]string{"method": "GET", "status": "200", "route": "/"}
		},
		"attribute moved from datapoint to resource": func(r *SeriesRow) {
			r.ResourceAttributes = map[string]string{"service.name": "api", "host.name": "web-1", "method": "GET"}
			r.DataPointAttributes = map[string]string{"status": "200"}
		},
		"string boundary shifted between key and value": func(r *SeriesRow) {
			r.DataPointAttributes = map[string]string{"methodG": "ET", "status": "200"}
		},
		"string boundary shifted between name and unit": func(r *SeriesRow) {
			r.MetricName, r.MetricUnit = "http.requests{", "request}"
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			base := baseSeriesRow()
			other := baseSeriesRow()
			mutate(&other)
			if metricHash(&base) == metricHash(&other) {
				t.Errorf("expected different MetricHash values")
			}
		})
	}
}
