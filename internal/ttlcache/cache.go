package ttlcache

import (
	"sync"
	"time"
)

type entry[V any] struct {
	value   V
	expires time.Time
}

// Cache is a bounded map whose entries expire. When full it drops expired entries first, then arbitrary ones.
type Cache[V any] struct {
	mu       sync.Mutex
	capacity int
	entries  map[string]entry[V]
}

func New[V any](capacity int) *Cache[V] {
	return &Cache[V]{capacity: capacity, entries: map[string]entry[V]{}}
}

func (c *Cache[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	item, found := c.entries[key]
	if !found || time.Now().After(item.expires) {
		var zero V
		return zero, false
	}
	return item.value, true
}

func (c *Cache[V]) Set(key string, value V, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.capacity {
		c.evict()
	}
	c.entries[key] = entry[V]{value, time.Now().Add(ttl)}
}

func (c *Cache[V]) evict() {
	now := time.Now()
	for key, item := range c.entries {
		if now.After(item.expires) {
			delete(c.entries, key)
		}
	}
	for key := range c.entries {
		if len(c.entries) < c.capacity {
			return
		}
		delete(c.entries, key)
	}
}
