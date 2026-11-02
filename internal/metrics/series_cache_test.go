package metrics

import (
	"testing"
	"time"
)

func TestSeriesCache(t *testing.T) {
	day := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC).Unix()
	nextDay := time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC).Unix()

	tests := map[string]struct {
		maxSize int
		added   []seriesKey
		present []seriesKey
		absent  []seriesKey
	}{
		"empty cache has no keys": {
			maxSize: 10,
			absent:  []seriesKey{{dayUnix: day, metricHash: 1}},
		},
		"added key is present": {
			maxSize: 10,
			added:   []seriesKey{{dayUnix: day, metricHash: 1}},
			present: []seriesKey{{dayUnix: day, metricHash: 1}},
			absent:  []seriesKey{{dayUnix: day, metricHash: 2}},
		},
		"same MetricHash on another Day is absent": {
			maxSize: 10,
			added:   []seriesKey{{dayUnix: day, metricHash: 1}},
			absent:  []seriesKey{{dayUnix: nextDay, metricHash: 1}},
		},
		"cache is cleared when full": {
			maxSize: 2,
			added:   []seriesKey{{dayUnix: day, metricHash: 1}, {dayUnix: day, metricHash: 2}, {dayUnix: day, metricHash: 3}},
			present: []seriesKey{{dayUnix: day, metricHash: 3}},
			absent:  []seriesKey{{dayUnix: day, metricHash: 1}, {dayUnix: day, metricHash: 2}},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cache := newSeriesCache(tt.maxSize)
			for _, key := range tt.added {
				cache.add(key)
			}

			for _, key := range tt.present {
				if !cache.has(key) {
					t.Errorf("want key %+v present", key)
				}
			}
			for _, key := range tt.absent {
				if cache.has(key) {
					t.Errorf("want key %+v absent", key)
				}
			}
		})
	}
}
