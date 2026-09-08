package redisstore

import (
	"fmt"
	"github.com/alicebob/miniredis/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestImmutableRecordConcurrentWriters(t *testing.T) {
	server := miniredis.RunT(t)
	store := New(Config{Addr: server.Addr(), Prefix: "immutable"})
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.SetIfAbsentOrEqual(t.Context(), "owner", []byte(fmt.Sprint(i)), time.Minute)
			if err != nil {
				t.Error(err)
			}
			if ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("winners=%d", wins.Load())
	}
	original, found, err := store.Get(t.Context(), "owner")
	if err != nil || !found {
		t.Fatal(err)
	}
	server.FastForward(30 * time.Second)
	if ok, err := store.SetIfAbsentOrEqual(t.Context(), "owner", original, time.Hour); err != nil || !ok {
		t.Fatalf("retry=%v %v", ok, err)
	}
	server.FastForward(31 * time.Second)
	if _, found, err := store.Get(t.Context(), "owner"); err != nil || found {
		t.Fatal("retry extended retention")
	}
}
