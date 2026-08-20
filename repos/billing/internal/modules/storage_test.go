package modules

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type retryWriter struct {
	attempts int
	done     chan BillingEvent
}

func (w *retryWriter) WriteUsageEvent(_ context.Context, event BillingEvent) error {
	w.attempts++
	if w.attempts == 1 {
		return errors.New("temporary")
	}
	w.done <- event
	return nil
}

func TestClickHouseUsageEventWriterWritesJSONEachRow(t *testing.T) {
	var receivedQuery string
	var receivedEvent BillingEvent

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedQuery = r.URL.Query().Get("query")
		if err := json.NewDecoder(r.Body).Decode(&receivedEvent); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	writer := NewClickHouseUsageEventWriter(Settings{
		ClickHouseURL:              server.URL,
		ClickHouseDatabase:         "ai_gateway",
		ClickHouseUsageEventsTable: "usage_events",
	})

	err := writer.WriteUsageEvent(context.Background(), BillingEvent{
		RequestID:       "req-1",
		UserID:          "user-1",
		TeamID:          "team-1",
		Provider:        "ollama",
		Model:           "test-model",
		Timestamp:       "2026-06-25T10:30:00Z",
		InputTokens:     10,
		OutputTokens:    5,
		TotalTokens:     15,
		CatalogVersion:  "catalog-v1",
		PricingKey:      "ollama/test-model",
		InputCostPer1M:  1,
		OutputCostPer1M: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(receivedQuery, "INSERT INTO ai_gateway.usage_events") {
		t.Fatalf("unexpected query: %s", receivedQuery)
	}
	if receivedEvent.UserID != "user-1" {
		t.Fatalf("unexpected event: %+v", receivedEvent)
	}
	if receivedEvent.Timestamp != "2026-06-25T10:30:00Z" {
		t.Fatalf("unexpected timestamp: %+v", receivedEvent)
	}
	if receivedEvent.TeamID != "team-1" || receivedEvent.CatalogVersion != "catalog-v1" || receivedEvent.PricingKey != "ollama/test-model" {
		t.Fatalf("pricing audit fields were not serialized: %+v", receivedEvent)
	}
}

func TestAsyncUsageOutboxRetriesDelivery(t *testing.T) {
	writer := &retryWriter{done: make(chan BillingEvent, 1)}
	outbox := NewAsyncUsageOutbox(writer, 1, 2)
	event := BillingEvent{RequestID: "req-async", Phase: "commit"}
	if err := outbox.WriteUsageEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	select {
	case delivered := <-writer.done:
		if delivered.RequestID != event.RequestID || writer.attempts != 2 {
			t.Fatalf("unexpected delivery: %+v attempts=%d", delivered, writer.attempts)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async retry")
	}
}

func TestEnabledPostgresPoliciesRequireStore(t *testing.T) {
	module := NewBillingModuleWithSettings(true, Settings{
		Pricing:        PricingConfig{Currency: "USD"},
		TariffsEnabled: true,
	})

	req := RequestContext{
		UserID: "user-1",
	}

	err := module.Handle(context.Background(), &req)
	if err == nil {
		t.Fatal("expected policy checker to reject missing postgres store")
	}
	if !strings.Contains(err.Error(), "postgres policy store") {
		t.Fatalf("unexpected error: %v", err)
	}
}
