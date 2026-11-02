package metrics

import "sync"

// seriesCache remembers the (Day, MetricHash) keys already written to the
// catalog by this instance, so each series is written once per day. When it
// is full it is cleared entirely.
type seriesCache struct {
	mu      sync.Mutex
	keys    map[seriesKey]struct{}
	maxSize int
}

func newSeriesCache(maxSize int) *seriesCache {
	return &seriesCache{keys: make(map[seriesKey]struct{}), maxSize: maxSize}
}

func (c *seriesCache) has(key seriesKey) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, ok := c.keys[key]
	return ok
}

func (c *seriesCache) add(key seriesKey) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.keys) >= c.maxSize {
		c.keys = make(map[seriesKey]struct{})
	}
	c.keys[key] = struct{}{}
}
