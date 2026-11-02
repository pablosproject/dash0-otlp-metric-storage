package metrics

import (
	"testing"
	"time"
)

func baseCatalogRow() CatalogRow {
	return CatalogRow{
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
	r := baseCatalogRow()
	const want uint64 = 13229911287843068379
	if got := metricHash(&r); got != want {
		t.Errorf("metricHash changed: got %d, want %d. A new encoding splits the history of every series.", got, want)
	}
}

func TestMetricHash_SameSeries(t *testing.T) {
	tests := map[string]func(r *CatalogRow){
		"attributes inserted in a different order": func(r *CatalogRow) {
			r.ResourceAttributes = map[string]string{"host.name": "web-1", "service.name": "api"}
		},
		"different scope": func(r *CatalogRow) {
			r.ScopeName, r.ScopeVersion = "other-lib", "2.0.0"
			r.ScopeAttributes = map[string]string{"k": "v"}
		},
		"different description": func(r *CatalogRow) { r.MetricDescription = "changed" },
		"different schema urls": func(r *CatalogRow) {
			r.ResourceSchemaUrl, r.ScopeSchemaUrl = "https://a", "https://b"
		},
		"different service name column": func(r *CatalogRow) { r.ServiceName = "other" },
		"different day":                 func(r *CatalogRow) { r.Day = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) },
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			base := baseCatalogRow()
			other := baseCatalogRow()
			mutate(&other)
			if metricHash(&base) != metricHash(&other) {
				t.Errorf("expected the same MetricHash")
			}
		})
	}
}

func TestMetricHash_NilAndEmptyAttributesAreEqual(t *testing.T) {
	a, b := baseCatalogRow(), baseCatalogRow()
	a.DataPointAttributes = nil
	b.DataPointAttributes = map[string]string{}
	if metricHash(&a) != metricHash(&b) {
		t.Errorf("expected nil and empty DataPointAttributes to give the same MetricHash")
	}
}

func TestMetricHash_DifferentSeries(t *testing.T) {
	tests := map[string]func(r *CatalogRow){
		"different metric name":  func(r *CatalogRow) { r.MetricName = "http.responses" },
		"different unit":         func(r *CatalogRow) { r.MetricUnit = "1" },
		"different type":         func(r *CatalogRow) { r.MetricType = MetricTypeGauge },
		"delta vs cumulative":    func(r *CatalogRow) { r.AggregationTemporality = 1 },
		"different monotonicity": func(r *CatalogRow) { r.IsMonotonic = false },
		"different resource attribute value": func(r *CatalogRow) {
			r.ResourceAttributes = map[string]string{"service.name": "api", "host.name": "web-2"}
		},
		"different datapoint attribute value": func(r *CatalogRow) {
			r.DataPointAttributes = map[string]string{"method": "GET", "status": "500"}
		},
		"extra datapoint attribute": func(r *CatalogRow) {
			r.DataPointAttributes = map[string]string{"method": "GET", "status": "200", "route": "/"}
		},
		"attribute moved from datapoint to resource": func(r *CatalogRow) {
			r.ResourceAttributes = map[string]string{"service.name": "api", "host.name": "web-1", "method": "GET"}
			r.DataPointAttributes = map[string]string{"status": "200"}
		},
		"string boundary shifted between key and value": func(r *CatalogRow) {
			r.DataPointAttributes = map[string]string{"methodG": "ET", "status": "200"}
		},
		"string boundary shifted between name and unit": func(r *CatalogRow) {
			r.MetricName, r.MetricUnit = "http.requests{", "request}"
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			base := baseCatalogRow()
			other := baseCatalogRow()
			mutate(&other)
			if metricHash(&base) == metricHash(&other) {
				t.Errorf("expected different MetricHash values")
			}
		})
	}
}
