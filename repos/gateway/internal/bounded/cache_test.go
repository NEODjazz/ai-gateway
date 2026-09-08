package bounded

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestCapacityExpiryAndReplacement(t *testing.T) {
	now := time.Unix(1, 0)
	c := New[string](time.Minute, 3, 12)
	for i := 0; i < 10000; i++ {
		c.Set(fmt.Sprint(i), "x", 1, now)
		entries, bytes := c.Size()
		if entries > 3 || bytes > 12 {
			t.Fatal("unbounded cache")
		}
	}
	c.Set("new", "x", 1, now.Add(2*time.Minute))
	if entries, _ := c.Size(); entries != 1 {
		t.Fatalf("expired entries retained: %d", entries)
	}
	c.Set("new", "xx", 2, now.Add(2*time.Minute))
	if entries, bytes := c.Size(); entries != 1 || bytes != 5 {
		t.Fatal("replacement accounting")
	}
	if _, ok := c.Get("new", now.Add(4*time.Minute)); ok {
		t.Fatal("expired hit")
	}
	c.Set("oversize", "too big", 100, now)
	if entries, _ := c.Size(); entries != 0 {
		t.Fatal("oversized entry stored")
	}
}
func TestConcurrentCache(t *testing.T) {
	c := New[string](time.Minute, 32, 1024)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				k := fmt.Sprintf("%d-%d", i, j)
				c.Set(k, "value", 5, time.Now())
				c.Get(k, time.Now())
			}
		}(i)
	}
	wg.Wait()
	if entries, bytes := c.Size(); entries > 32 || bytes > 1024 {
		t.Fatal("capacity exceeded")
	}
}
