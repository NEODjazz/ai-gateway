package controlstore

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/realtimestate"
)

func TestPostgresRealtimeBrowserTicketIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	migration, err := os.ReadFile("../../../../migrations/postgres/040_gateway_realtime_browser_tickets.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(t.Context(), string(migration)); err != nil {
		t.Fatal(err)
	}
	replica, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(replica.Close)
	ticket := realtimestate.Ticket{Hash: fmt.Sprintf("%064x", 1), OwnerKey: fmt.Sprintf("%064x", 1), Model: "model", Origin: "http://gateway.test", Payload: []byte("encrypted-fixture"), ExpiresAt: time.Now().Add(realtimestate.TicketTTL).Truncate(time.Microsecond)}
	if err := store.CreateRealtimeTicket(t.Context(), ticket); err != nil {
		t.Fatal(err)
	}
	if _, err := replica.ConsumeRealtimeTicket(t.Context(), ticket.Hash, "other", ticket.Origin); !errors.Is(err, realtimestate.ErrNotFound) {
		t.Fatalf("model binding: %v", err)
	}
	if _, err := replica.ConsumeRealtimeTicket(t.Context(), ticket.Hash, ticket.Model, "http://other.test"); !errors.Is(err, realtimestate.ErrNotFound) {
		t.Fatalf("origin binding: %v", err)
	}
	var group sync.WaitGroup
	type outcome struct {
		ticket realtimestate.Ticket
		err    error
	}
	results := make(chan outcome, 8)
	for index := range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			target := store
			if index%2 == 0 {
				target = replica
			}
			item, err := target.ConsumeRealtimeTicket(t.Context(), ticket.Hash, ticket.Model, ticket.Origin)
			results <- outcome{item, err}
		}()
	}
	group.Wait()
	close(results)
	winners := 0
	for result := range results {
		if result.err == nil {
			winners++
			item := result.ticket
			if item.Hash != ticket.Hash || item.OwnerKey != ticket.OwnerKey || item.Model != ticket.Model || item.Origin != ticket.Origin || !item.ExpiresAt.Equal(ticket.ExpiresAt) || !bytes.Equal(item.Payload, ticket.Payload) {
				t.Fatal("PostgreSQL changed ticket ciphertext or authenticated binding fields")
			}
		} else if !errors.Is(result.err, realtimestate.ErrNotFound) {
			t.Fatal(result.err)
		}
	}
	if winners != 1 {
		t.Fatalf("cross-replica winners=%d", winners)
	}
	for index := range realtimestate.MaxOwnerTickets {
		ticket.Hash = fmt.Sprintf("%064x", index+2)
		ticket.ExpiresAt = time.Now().Add(realtimestate.TicketTTL).Truncate(time.Microsecond)
		if err := store.CreateRealtimeTicket(t.Context(), ticket); err != nil {
			t.Fatal(err)
		}
	}
	ticket.Hash = fmt.Sprintf("%064x", 50)
	if err := replica.CreateRealtimeTicket(t.Context(), ticket); !errors.Is(err, realtimestate.ErrCapacity) {
		t.Fatalf("owner admission: %v", err)
	}
	if _, err := store.pool.Exec(t.Context(), `UPDATE gateway_realtime_browser_tickets SET expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := replica.ConsumeRealtimeTicket(t.Context(), fmt.Sprintf("%064x", 2), ticket.Model, ticket.Origin); !errors.Is(err, realtimestate.ErrNotFound) {
		t.Fatalf("expiry: %v", err)
	}
	ticket.ExpiresAt = time.Now().Add(realtimestate.TicketTTL).Truncate(time.Microsecond)
	if err := store.CreateRealtimeTicket(t.Context(), ticket); err != nil {
		t.Fatalf("expired quota release: %v", err)
	}
	if _, err := store.pool.Exec(t.Context(), `INSERT INTO gateway_realtime_browser_tickets(ticket_hash,owner_key,model,origin,payload,expires_at) SELECT lpad(to_hex(value),64,'0'),lpad(to_hex(value),64,'0'),'model','http://gateway.test','encrypted-fixture'::bytea,now()+interval '30 seconds' FROM generate_series(100,1122) value`); err != nil {
		t.Fatal(err)
	}
	ticket.OwnerKey = fmt.Sprintf("%064x", 9999)
	ticket.Hash = fmt.Sprintf("%064x", 9999)
	if err := replica.CreateRealtimeTicket(t.Context(), ticket); !errors.Is(err, realtimestate.ErrCapacity) {
		t.Fatalf("global admission: %v", err)
	}
}
