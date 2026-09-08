package modules

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestLifecycleCapacityRejectsAndExpires(t *testing.T) {
	s := NewLifecycleStore()
	s.capacity = 2
	now := time.Unix(1, 0)
	s.now = func() time.Time { return now }
	for _, key := range []string{"a", "b"} {
		if created, err := s.Claim(key); err != nil || !created {
			t.Fatal("claim failed")
		}
	}
	if created, err := s.Claim("c"); created || err == nil {
		t.Fatal("live dedup eviction allowed")
	}
	if created, err := s.Claim("a"); created || err != nil {
		t.Fatal("duplicate not retained")
	}
	now = now.Add(s.ttl)
	if created, err := s.Claim("c"); !created || err != nil {
		t.Fatal("expired entries not reclaimed")
	}
	if len(s.seen) != 1 {
		t.Fatal("unbounded history")
	}
}
func TestLifecycleConcurrentDuplicate(t *testing.T) {
	s := NewLifecycleStore()
	var wg sync.WaitGroup
	var mu sync.Mutex
	count := 0
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			created, err := s.Claim("same")
			if err != nil {
				t.Error(err)
			}
			if created {
				mu.Lock()
				count++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if count != 1 {
		t.Fatal("duplicate claim")
	}
}

type failedUsageWriter struct{}

func (failedUsageWriter) WriteUsageEvent(context.Context, BillingEvent) error {
	return errors.New("delivery failed")
}
func TestMemoryOutboxAccountsPermanentFailureAndCloses(t *testing.T) {
	o := NewAsyncUsageOutbox(failedUsageWriter{}, 10, 0)
	for i := 0; i < 3; i++ {
		if err := o.WriteUsageEvent(context.Background(), BillingEvent{RequestID: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := o.Close(ctx); err != nil {
		t.Fatal(err)
	}
	stats := o.Stats()
	if stats.Accepted != 3 || stats.Failed != 3 || stats.Delivered != 0 || stats.Queued != 0 {
		t.Fatalf("stats=%+v", stats)
	}
	if err := o.WriteUsageEvent(context.Background(), BillingEvent{}); err == nil {
		t.Fatal("closed queue accepted event")
	}
	if o.Stats().Rejected != 1 {
		t.Fatal("rejection not counted")
	}
}

type cancelUsageWriter struct{ started chan struct{} }

func (w cancelUsageWriter) WriteUsageEvent(ctx context.Context, _ BillingEvent) error {
	close(w.started)
	<-ctx.Done()
	return ctx.Err()
}
func TestMemoryOutboxCancellationAccountsPendingEvents(t *testing.T) {
	started := make(chan struct{})
	o := NewAsyncUsageOutbox(cancelUsageWriter{started}, 1, 0)
	if err := o.WriteUsageEvent(context.Background(), BillingEvent{}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := o.WriteUsageEvent(context.Background(), BillingEvent{}); err != nil {
		t.Fatal(err)
	}
	if err := o.WriteUsageEvent(context.Background(), BillingEvent{}); err == nil {
		t.Fatal("full queue accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := o.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	stats := o.Stats()
	if stats.Dropped != 2 || stats.Accepted != 2 || stats.Rejected != 1 {
		t.Fatalf("stats=%+v", stats)
	}
}
func TestRequiredUsageCannotUseMemoryOutbox(t *testing.T) {
	m := NewBillingModuleWithSettings(true, Settings{UsageEventsEnabled: true})
	defer m.Close()
	if m.Ready(context.Background()) == nil {
		t.Fatal("required usage allowed without durability")
	}
	if _, ok := m.writer.(*AsyncUsageOutbox); ok {
		t.Fatal("unusable worker started")
	}
}
