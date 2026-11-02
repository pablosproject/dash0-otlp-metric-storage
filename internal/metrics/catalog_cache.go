package metrics

import "sync"

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

func (c *catalogCache) add(key catalogKey) (cleared bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.keys) >= c.maxSize {
		c.keys = make(map[catalogKey]struct{})
		cleared = true
	}
	c.keys[key] = struct{}{}
	return cleared
}
