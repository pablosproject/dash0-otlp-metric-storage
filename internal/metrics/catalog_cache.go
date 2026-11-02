package metrics

import "sync"

// catalogCache remembers the (Day, MetricHash) keys already written to the
// catalog by this instance, so each series is written once per day. When it
// is full it is cleared entirely.
type catalogCache struct {
	mu      sync.Mutex
	keys    map[catalogKey]struct{}
	maxSize int
}

func newCatalogCache(maxSize int) *catalogCache {
	return &catalogCache{keys: make(map[catalogKey]struct{}), maxSize: maxSize}
}

func (c *catalogCache) has(key catalogKey) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, ok := c.keys[key]
	return ok
}

func (c *catalogCache) add(key catalogKey) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.keys) >= c.maxSize {
		c.keys = make(map[catalogKey]struct{})
	}
	c.keys[key] = struct{}{}
}
