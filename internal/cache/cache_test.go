package cache

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	t.Parallel()
	c := New[string](time.Minute, 100)
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if c.ttl != time.Minute {
		t.Errorf("New() ttl = %v, want %v", c.ttl, time.Minute)
	}
	if c.maxEntries != 100 {
		t.Errorf("New() maxEntries = %d, want 100", c.maxEntries)
	}
	if c.entries == nil {
		t.Error("New() entries map is nil")
	}
}

func TestGetSet(t *testing.T) {
	t.Parallel()
	c := New[string](time.Minute, 0)

	// Test missing key
	val, ok := c.Get("missing")
	if ok {
		t.Error("Get() on missing key should return false")
	}
	if val != "" {
		t.Errorf("Get() on missing key should return zero value, got %q", val)
	}

	// Test Set and Get
	c.Set("key1", "value1")
	val, ok = c.Get("key1")
	if !ok {
		t.Error("Get() on existing key should return true")
	}
	if val != "value1" {
		t.Errorf("Get() = %q, want %q", val, "value1")
	}

	// Test overwrite
	c.Set("key1", "value2")
	val, ok = c.Get("key1")
	if !ok {
		t.Error("Get() after overwrite should return true")
	}
	if val != "value2" {
		t.Errorf("Get() after overwrite = %q, want %q", val, "value2")
	}
}

func TestGetExpired(t *testing.T) {
	t.Parallel()
	// Use very short TTL for testing expiration
	c := New[string](50*time.Millisecond, 0)

	c.Set("key1", "value1")

	// Should exist immediately
	val, ok := c.Get("key1")
	if !ok {
		t.Error("Get() immediately after Set should return true")
	}
	if val != "value1" {
		t.Errorf("Get() = %q, want %q", val, "value1")
	}

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Should be expired now
	val, ok = c.Get("key1")
	if ok {
		t.Error("Get() on expired key should return false")
	}
	if val != "" {
		t.Errorf("Get() on expired key should return zero value, got %q", val)
	}
}

func TestDelete(t *testing.T) {
	t.Parallel()
	c := New[string](time.Minute, 0)

	c.Set("key1", "value1")
	c.Set("key2", "value2")

	// Delete one key
	c.Delete("key1")

	// key1 should be gone
	_, ok := c.Get("key1")
	if ok {
		t.Error("Get() after Delete should return false")
	}

	// key2 should still exist
	val, ok := c.Get("key2")
	if !ok {
		t.Error("Get() on non-deleted key should return true")
	}
	if val != "value2" {
		t.Errorf("Get() = %q, want %q", val, "value2")
	}

	// Delete non-existent key (should not panic)
	c.Delete("nonexistent")
}

func TestClear(t *testing.T) {
	t.Parallel()
	c := New[string](time.Minute, 0)

	c.Set("key1", "value1")
	c.Set("key2", "value2")
	c.Set("key3", "value3")

	c.Clear()

	// All keys should be gone
	for _, key := range []string{"key1", "key2", "key3"} {
		_, ok := c.Get(key)
		if ok {
			t.Errorf("Get(%q) after Clear should return false", key)
		}
	}

	// Should be able to add new entries after clear
	c.Set("key4", "value4")
	val, ok := c.Get("key4")
	if !ok {
		t.Error("Get() after Clear+Set should return true")
	}
	if val != "value4" {
		t.Errorf("Get() = %q, want %q", val, "value4")
	}
}

func TestCacheWithDifferentTypes(t *testing.T) {
	t.Parallel()
	t.Run("int cache", func(t *testing.T) {
		c := New[int](time.Minute, 0)
		c.Set("count", 42)
		val, ok := c.Get("count")
		if !ok || val != 42 {
			t.Errorf("int cache Get() = %d, %v, want 42, true", val, ok)
		}
	})

	t.Run("struct cache", func(t *testing.T) {
		type Item struct {
			ID   string
			Name string
		}
		c := New[Item](time.Minute, 0)
		c.Set("item1", Item{ID: "1", Name: "First"})
		val, ok := c.Get("item1")
		if !ok {
			t.Fatal("struct cache Get() should return true")
		}
		if val.ID != "1" || val.Name != "First" {
			t.Errorf("struct cache Get() = %+v, want {ID:1 Name:First}", val)
		}
	})

	t.Run("slice cache", func(t *testing.T) {
		c := New[[]string](time.Minute, 0)
		c.Set("items", []string{"a", "b", "c"})
		val, ok := c.Get("items")
		if !ok {
			t.Fatal("slice cache Get() should return true")
		}
		if len(val) != 3 || val[0] != "a" {
			t.Errorf("slice cache Get() = %v, want [a b c]", val)
		}
	})

	t.Run("pointer cache", func(t *testing.T) {
		type Data struct {
			Value int
		}
		c := New[*Data](time.Minute, 0)
		d := &Data{Value: 100}
		c.Set("ptr", d)
		val, ok := c.Get("ptr")
		if !ok {
			t.Fatal("pointer cache Get() should return true")
		}
		if val.Value != 100 {
			t.Errorf("pointer cache Get() = %+v, want &{Value:100}", val)
		}
	})
}

func TestConcurrentAccess(t *testing.T) {
	t.Parallel()
	c := New[int](time.Minute, 0)
	var wg sync.WaitGroup
	numGoroutines := 100
	numOps := 100

	// Concurrent writes
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOps; j++ {
				key := "key"
				c.Set(key, id*numOps+j)
			}
		}(i)
	}
	wg.Wait()

	// Concurrent reads
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < numOps; j++ {
				c.Get("key")
			}
		}()
	}
	wg.Wait()

	// Concurrent mixed operations
	wg.Add(numGoroutines * 4)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			c.Set("mixed", id)
		}(i)
		go func() {
			defer wg.Done()
			c.Get("mixed")
		}()
		go func() {
			defer wg.Done()
			c.Delete("mixed")
		}()
		go func() {
			defer wg.Done()
			c.Clear()
		}()
	}
	wg.Wait()
	// Test passes if no race conditions or panics occur
}

func TestZeroValueTypes(t *testing.T) {
	t.Parallel()
	t.Run("string zero value", func(t *testing.T) {
		c := New[string](time.Minute, 0)
		val, ok := c.Get("missing")
		if ok {
			t.Error("Get() should return false for missing key")
		}
		if val != "" {
			t.Errorf("Get() should return empty string, got %q", val)
		}
	})

	t.Run("int zero value", func(t *testing.T) {
		c := New[int](time.Minute, 0)
		val, ok := c.Get("missing")
		if ok {
			t.Error("Get() should return false for missing key")
		}
		if val != 0 {
			t.Errorf("Get() should return 0, got %d", val)
		}
	})

	t.Run("pointer zero value", func(t *testing.T) {
		type Data struct {
			Value int
		}
		c := New[*Data](time.Minute, 0)
		val, ok := c.Get("missing")
		if ok {
			t.Error("Get() should return false for missing key")
		}
		if val != nil {
			t.Errorf("Get() should return nil, got %v", val)
		}
	})

	t.Run("slice zero value", func(t *testing.T) {
		c := New[[]string](time.Minute, 0)
		val, ok := c.Get("missing")
		if ok {
			t.Error("Get() should return false for missing key")
		}
		if val != nil {
			t.Errorf("Get() should return nil, got %v", val)
		}
	})
}

func TestMultipleKeys(t *testing.T) {
	t.Parallel()
	c := New[string](time.Minute, 0)

	// Set multiple keys
	keys := []string{"a", "b", "c", "d", "e"}
	for i, key := range keys {
		c.Set(key, key+"-value")
		_ = i
	}

	// Verify all keys exist
	for _, key := range keys {
		val, ok := c.Get(key)
		if !ok {
			t.Errorf("Get(%q) should return true", key)
		}
		expected := key + "-value"
		if val != expected {
			t.Errorf("Get(%q) = %q, want %q", key, val, expected)
		}
	}

	// Delete some keys
	c.Delete("b")
	c.Delete("d")

	// Verify deleted keys are gone
	for _, key := range []string{"b", "d"} {
		_, ok := c.Get(key)
		if ok {
			t.Errorf("Get(%q) after delete should return false", key)
		}
	}

	// Verify remaining keys still exist
	for _, key := range []string{"a", "c", "e"} {
		val, ok := c.Get(key)
		if !ok {
			t.Errorf("Get(%q) should still return true", key)
		}
		expected := key + "-value"
		if val != expected {
			t.Errorf("Get(%q) = %q, want %q", key, val, expected)
		}
	}
}

func TestMaxEntriesEviction(t *testing.T) {
	t.Parallel()
	// Create cache with max 3 entries
	c := New[string](time.Minute, 3)

	// Add 3 entries with small delays to ensure different expiry times
	c.Set("key1", "value1")
	time.Sleep(10 * time.Millisecond)
	c.Set("key2", "value2")
	time.Sleep(10 * time.Millisecond)
	c.Set("key3", "value3")

	// All 3 should exist
	for _, key := range []string{"key1", "key2", "key3"} {
		if _, ok := c.Get(key); !ok {
			t.Errorf("Get(%q) should return true before eviction", key)
		}
	}

	// Add 4th entry - should evict key1 (oldest by expiry)
	c.Set("key4", "value4")

	// key1 should be evicted (oldest expiry)
	if _, ok := c.Get("key1"); ok {
		t.Error("key1 should have been evicted")
	}

	// key2, key3, key4 should still exist
	for _, key := range []string{"key2", "key3", "key4"} {
		if _, ok := c.Get(key); !ok {
			t.Errorf("Get(%q) should return true after eviction", key)
		}
	}
}

func TestMaxEntriesOverwriteNoEviction(t *testing.T) {
	t.Parallel()
	// Create cache with max 2 entries
	c := New[string](time.Minute, 2)

	c.Set("key1", "value1")
	c.Set("key2", "value2")

	// Overwriting existing key should NOT trigger eviction
	c.Set("key1", "value1-updated")

	// Both keys should still exist
	val1, ok1 := c.Get("key1")
	val2, ok2 := c.Get("key2")

	if !ok1 || val1 != "value1-updated" {
		t.Errorf("key1 should exist with updated value, got %q, %v", val1, ok1)
	}
	if !ok2 || val2 != "value2" {
		t.Errorf("key2 should exist, got %q, %v", val2, ok2)
	}
}

func TestMaxEntriesZeroMeansUnlimited(t *testing.T) {
	t.Parallel()
	// maxEntries=0 should mean unlimited
	c := New[string](time.Minute, 0)

	// Add many entries
	for i := 0; i < 100; i++ {
		c.Set(fmt.Sprintf("key%d", i), fmt.Sprintf("value%d", i))
	}

	// All should exist
	for i := 0; i < 100; i++ {
		if _, ok := c.Get(fmt.Sprintf("key%d", i)); !ok {
			t.Errorf("key%d should exist with unlimited cache", i)
		}
	}
}
