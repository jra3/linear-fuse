package cache

import (
	"strings"
	"sync"
	"time"
)

type entry[T any] struct {
	value     T
	expiresAt time.Time
}

type Cache[T any] struct {
	mu      sync.RWMutex
	entries map[string]entry[T]
	ttl     time.Duration
	stopCh  chan struct{}
}

func New[T any](ttl time.Duration) *Cache[T] {
	c := &Cache[T]{
		entries: make(map[string]entry[T]),
		ttl:     ttl,
		stopCh:  make(chan struct{}),
	}

	// Start background cleanup goroutine
	go c.cleanup()

	return c
}

func (c *Cache[T]) Get(key string) (T, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	e, ok := c.entries[key]
	if !ok {
		var zero T
		return zero, false
	}

	if time.Now().After(e.expiresAt) {
		var zero T
		return zero, false
	}

	return e.value, true
}

func (c *Cache[T]) Set(key string, value T) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = entry[T]{
		value:     value,
		expiresAt: time.Now().Add(c.ttl),
	}
}

func (c *Cache[T]) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.entries, key)
}

func (c *Cache[T]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = make(map[string]entry[T])
}

// DeleteByPrefix removes all cache entries whose keys start with the given prefix
func (c *Cache[T]) DeleteByPrefix(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key := range c.entries {
		if strings.HasPrefix(key, prefix) {
			delete(c.entries, key)
		}
	}
}

// Stop terminates the background cleanup goroutine
func (c *Cache[T]) Stop() {
	close(c.stopCh)
}

func (c *Cache[T]) cleanup() {
	ticker := time.NewTicker(c.ttl)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.mu.Lock()
			now := time.Now()
			for key, e := range c.entries {
				if now.After(e.expiresAt) {
					delete(c.entries, key)
				}
			}
			c.mu.Unlock()
		case <-c.stopCh:
			return
		}
	}
}
