package realtimestate

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func memoryTicket(now time.Time, id, owner int) Ticket {
	return Ticket{Hash: fmt.Sprintf("%064x", id), OwnerKey: fmt.Sprintf("%064x", owner), Model: "model", Origin: "http://gateway.test", Payload: []byte("encrypted-fixture"), ExpiresAt: now.Add(TicketTTL)}
}

func TestMemoryTicketBoundsExpiryAndBinding(t *testing.T) {
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	store := NewMemoryStore(2, 1)
	store.now = func() time.Time { return now }
	first := memoryTicket(now, 1, 1)
	if err := store.CreateRealtimeTicket(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	first.Payload[0] = 'X'
	if err := store.CreateRealtimeTicket(t.Context(), memoryTicket(now, 2, 1)); !errors.Is(err, ErrCapacity) {
		t.Fatalf("owner limit: %v", err)
	}
	if err := store.CreateRealtimeTicket(t.Context(), memoryTicket(now, 2, 2)); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRealtimeTicket(t.Context(), memoryTicket(now, 3, 3)); !errors.Is(err, ErrCapacity) {
		t.Fatalf("global limit: %v", err)
	}
	if _, err := store.ConsumeRealtimeTicket(t.Context(), first.Hash, "other", first.Origin); !errors.Is(err, ErrNotFound) {
		t.Fatalf("model binding: %v", err)
	}
	if _, err := store.ConsumeRealtimeTicket(t.Context(), first.Hash, first.Model, "http://other.test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("origin binding: %v", err)
	}
	consumed, err := store.ConsumeRealtimeTicket(t.Context(), first.Hash, first.Model, first.Origin)
	if err != nil || string(consumed.Payload) != "encrypted-fixture" {
		t.Fatalf("consume/copy: %v", err)
	}
	if _, err := store.ConsumeRealtimeTicket(t.Context(), first.Hash, first.Model, first.Origin); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replay: %v", err)
	}
	now = now.Add(TicketTTL)
	if err := store.CreateRealtimeTicket(t.Context(), memoryTicket(now, 3, 3)); err != nil {
		t.Fatal(err)
	}
	if len(store.tickets) != 1 {
		t.Fatalf("expired tickets retained: %d", len(store.tickets))
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.CreateRealtimeTicket(ctx, memoryTicket(now, 4, 4)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled admission: %v", err)
	}
	if _, err := store.ConsumeRealtimeTicket(ctx, fmt.Sprintf("%064x", 3), "model", "http://gateway.test"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled consume: %v", err)
	}
}

func TestMemoryTicketSingleUseIsAtomic(t *testing.T) {
	store := NewMemoryStore(10, 8)
	ticket := memoryTicket(time.Now(), 1, 1)
	if err := store.CreateRealtimeTicket(t.Context(), ticket); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 16)
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := store.ConsumeRealtimeTicket(t.Context(), ticket.Hash, ticket.Model, ticket.Origin)
			results <- err
		}()
	}
	group.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("consumption winners=%d", winners)
	}
}

func TestMemoryTicketRejectsInvalidRecords(t *testing.T) {
	now := time.Now()
	valid := memoryTicket(now, 1, 1)
	for _, mutate := range []func(*Ticket){func(t *Ticket) { t.Hash = "invalid" }, func(t *Ticket) { t.OwnerKey = "invalid" }, func(t *Ticket) { t.Payload = make([]byte, MaxPayloadBytes+1) }, func(t *Ticket) { t.ExpiresAt = now }, func(t *Ticket) { t.ExpiresAt = now.Add(TicketTTL + time.Second) }} {
		item := valid
		mutate(&item)
		if Valid(item, now) {
			t.Fatal("invalid ticket accepted")
		}
	}
}
