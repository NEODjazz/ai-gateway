package modules

import (
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

const lifecycleMaxEntries = 10000
const lifecycleTTL = 15 * time.Minute

type lifecycleEntry struct {
	key     [32]byte
	expires time.Time
}
type LifecycleStore struct {
	mu       sync.Mutex
	seen     map[[32]byte]*list.Element
	order    list.List
	now      func() time.Time
	capacity int
	ttl      time.Duration
}

func NewLifecycleStore() *LifecycleStore {
	return &LifecycleStore{seen: map[[32]byte]*list.Element{}, now: time.Now, capacity: lifecycleMaxEntries, ttl: lifecycleTTL}
}

// Claim refuses new work at capacity rather than evicting a live dedup marker.
func (s *LifecycleStore) Claim(key string) (bool, error) {
	if s == nil || key == "" {
		return true, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for e := s.order.Front(); e != nil; e = s.order.Front() {
		v := e.Value.(lifecycleEntry)
		if now.Before(v.expires) {
			break
		}
		delete(s.seen, v.key)
		s.order.Remove(e)
	}
	hash := sha256.Sum256([]byte(key))
	if _, ok := s.seen[hash]; ok {
		return false, nil
	}
	if len(s.seen) >= s.capacity {
		return false, errors.New("billing lifecycle capacity exceeded")
	}
	s.seen[hash] = s.order.PushBack(lifecycleEntry{hash, now.Add(s.ttl)})
	return true, nil
}
func (s *LifecycleStore) Begin(key string) bool { created, _ := s.Claim(key); return created }
func (s *LifecycleStore) Release(key string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hash := sha256.Sum256([]byte(key))
	if e := s.seen[hash]; e != nil {
		delete(s.seen, hash)
		s.order.Remove(e)
	}
}

type UsageDeliveryStats struct {
	Accepted, Delivered, Failed, Dropped, Rejected uint64
	Queued                                         int
}
type AsyncUsageOutbox struct {
	writer                                         UsageEventWriter
	queue                                          chan BillingEvent
	retries                                        int
	mu                                             sync.RWMutex
	closed                                         bool
	cancel                                         context.CancelFunc
	done                                           chan struct{}
	accepted, delivered, failed, dropped, rejected atomic.Uint64
}

func NewAsyncUsageOutbox(writer UsageEventWriter, capacity, retries int) *AsyncUsageOutbox {
	if capacity <= 0 {
		capacity = 1024
	}
	retries = max(0, min(10, retries))
	ctx, cancel := context.WithCancel(context.Background())
	o := &AsyncUsageOutbox{writer: writer, queue: make(chan BillingEvent, capacity), retries: retries, cancel: cancel, done: make(chan struct{})}
	go o.run(ctx)
	return o
}
func (o *AsyncUsageOutbox) WriteUsageEvent(ctx context.Context, event BillingEvent) error {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		o.rejected.Add(1)
		return err
	}
	if o.closed {
		o.rejected.Add(1)
		return errors.New("usage outbox is closed")
	}
	select {
	case o.queue <- event:
		o.accepted.Add(1)
		return nil
	default:
		o.rejected.Add(1)
		return errors.New("usage outbox is full")
	}
}
func (o *AsyncUsageOutbox) Stats() UsageDeliveryStats {
	return UsageDeliveryStats{o.accepted.Load(), o.delivered.Load(), o.failed.Load(), o.dropped.Load(), o.rejected.Load(), len(o.queue)}
}
func (o *AsyncUsageOutbox) Close(ctx context.Context) error {
	o.mu.Lock()
	if !o.closed {
		o.closed = true
		close(o.queue)
	}
	o.mu.Unlock()
	select {
	case <-o.done:
		o.cancel()
		return nil
	case <-ctx.Done():
		o.cancel()
		<-o.done
		return ctx.Err()
	}
}
func (o *AsyncUsageOutbox) run(ctx context.Context) {
	defer close(o.done)
	for event := range o.queue {
		if ctx.Err() != nil {
			o.dropped.Add(1)
			continue
		}
		delivered := false
		for attempt := 0; attempt <= o.retries; attempt++ {
			callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := o.writer.WriteUsageEvent(callCtx, event)
			cancel()
			if err == nil {
				o.delivered.Add(1)
				delivered = true
				break
			}
			if ctx.Err() != nil {
				break
			}
			if attempt < o.retries {
				timer := time.NewTimer(time.Duration(attempt+1) * 50 * time.Millisecond)
				select {
				case <-timer.C:
				case <-ctx.Done():
				}
				timer.Stop()
			}
		}
		if !delivered {
			if ctx.Err() != nil {
				o.dropped.Add(1)
			} else {
				o.failed.Add(1)
				log.Print("billing memory outbox exhausted delivery retries; usage event lost")
			}
		}
	}
	if n := o.dropped.Load(); n > 0 {
		log.Printf("billing memory outbox shutdown dropped %d events", n)
	}
}

func (m BillingModule) UsageDeliveryStats() UsageDeliveryStats {
	if o, ok := m.writer.(*AsyncUsageOutbox); ok {
		return o.Stats()
	}
	return UsageDeliveryStats{}
}
