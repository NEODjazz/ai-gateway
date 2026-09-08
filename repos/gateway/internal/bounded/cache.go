// Package bounded provides a byte- and entry-bounded FIFO cache with TTL expiry.
package bounded

import (
	"container/list"
	"sync"
	"time"
)

type entry[T any] struct {
	key     string
	value   T
	expires time.Time
	bytes   int
}
type Cache[T any] struct {
	mu                          sync.Mutex
	entries                     map[string]*list.Element
	order                       list.List
	bytes, maxBytes, maxEntries int
	ttl                         time.Duration
}

func New[T any](ttl time.Duration, entries, bytes int) *Cache[T] {
	return &Cache[T]{entries: make(map[string]*list.Element), ttl: ttl, maxEntries: entries, maxBytes: bytes}
}
func (c *Cache[T]) remove(e *list.Element) {
	v := e.Value.(entry[T])
	delete(c.entries, v.key)
	c.bytes -= v.bytes
	c.order.Remove(e)
}
func (c *Cache[T]) prune(now time.Time) {
	for e := c.order.Front(); e != nil; e = c.order.Front() {
		if now.Before(e.Value.(entry[T]).expires) {
			break
		}
		c.remove(e)
	}
}
func (c *Cache[T]) Get(key string, now time.Time) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune(now)
	if e := c.entries[key]; e != nil {
		if !now.Before(e.Value.(entry[T]).expires) {
			c.remove(e)
			var zero T
			return zero, false
		}
		return e.Value.(entry[T]).value, true
	}
	var zero T
	return zero, false
}
func (c *Cache[T]) Set(key string, value T, size int, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune(now)
	size += len(key)
	if e := c.entries[key]; e != nil {
		c.remove(e)
	}
	if size > c.maxBytes || c.maxEntries <= 0 || c.ttl <= 0 {
		return
	}
	for len(c.entries) >= c.maxEntries || c.bytes+size > c.maxBytes {
		c.remove(c.order.Front())
	}
	c.entries[key] = c.order.PushBack(entry[T]{key, value, now.Add(c.ttl), size})
	c.bytes += size
}
func (c *Cache[T]) Size() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries), c.bytes
}
