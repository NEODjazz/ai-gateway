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
		RequestID:              "req-1",
		UserID:                 "user-1",
		TeamID:                 "team-1",
		Tags:                   []string{"production", "cost-center-a"},
		Provider:               "ollama",
		Model:                  "test-model",
		Timestamp:              "2026-06-25T10:30:00Z",
		InputTokens:            10,
		OutputTokens:           5,
		TrainingTokens:         1000,
		TotalTokens:            15,
		InputCharacters:        4096,
		InputPages:             4,
		InputAudioMilliseconds: 90000,
		ToolRequests:           3,
		CacheReadInputTokens:   7,
		CacheWriteInputTokens:  3,
		CatalogVersion:         "catalog-v1",
		PricingKey:             "ollama/test-model",
		PageCostPer1K:          100,
		InputCostPer1M:         1,
		OutputCostPer1M:        2,
		TrainingCostPer1M:      5,
		SearchRequests:         2,
		SearchCostPer1K:        10,
		CharacterCostPer1M:     15,
		AudioCostPerMinute:     0.12,
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
	if receivedEvent.TeamID != "team-1" || len(receivedEvent.Tags) != 2 || receivedEvent.Tags[0] != "production" || receivedEvent.CacheReadInputTokens != 7 || receivedEvent.CacheWriteInputTokens != 3 || receivedEvent.CatalogVersion != "catalog-v1" || receivedEvent.PricingKey != "ollama/test-model" || receivedEvent.TrainingTokens != 1000 || receivedEvent.TrainingCostPer1M != 5 || receivedEvent.SearchRequests != 2 || receivedEvent.SearchCostPer1K != 10 || receivedEvent.InputCharacters != 4096 || receivedEvent.CharacterCostPer1M != 15 || receivedEvent.InputPages != 4 || receivedEvent.PageCostPer1K != 100 || receivedEvent.InputAudioMilliseconds != 90000 || receivedEvent.ToolRequests != 3 || receivedEvent.AudioCostPerMinute != 0.12 {
		t.Fatalf("pricing audit fields were not serialized: %+v", receivedEvent)
	}
}

func TestAsyncUsageOutboxRetriesDelivery(t *testing.T) {
	writer := &retryWriter{done: make(chan BillingEvent, 1)}
	outbox := NewAsyncUsageOutbox(writer, 1, 2)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := outbox.Close(ctx); err != nil {
			t.Error(err)
		}
	})
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
