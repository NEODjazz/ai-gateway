package modules

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"

	"ai-gateway-billing/internal/openai"
)

func TestModelCatalogPricingPrecedenceAndAuditFields(t *testing.T) {
	catalogJSON := `{
		"version":"2026-08-21.1","unknown_model_policy":"deny","models":[
			{"provider":"openai","model":"gpt-test","input_cost_per_1m":1,"output_cost_per_1m":2,"currency":"USD"},
			{"provider":"endpoint-a","model":"gpt-test","input_cost_per_1m":2,"output_cost_per_1m":4,"currency":"USD"}
		]}`
	module := NewBillingModuleWithSettings(true, Settings{
		Pricing: PricingConfig{Currency: "USD"}, ModelCatalogJSON: catalogJSON,
	})
	req := RequestContext{
		RequestID: "catalog-pricing", BillingPhase: "commit", PostResponse: true,
		Request: openai.ChatCompletionRequest{Provider: "openai", Model: "gpt-test"},
		Usage:   &openai.Usage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500},
		Metadata: map[string]string{
			"provider.endpoint.name": "endpoint-a", "provider.endpoint.type": "openai",
		},
	}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if req.BillingEvent == nil || req.BillingEvent.CatalogVersion != "2026-08-21.1" || req.BillingEvent.PricingKey != "endpoint-a/gpt-test" {
		t.Fatalf("missing catalog audit fields: %+v", req.BillingEvent)
	}
	if math.Abs(req.BillingEvent.Cost-0.004) > 1e-12 || req.BillingEvent.InputCostPer1M != 2 || req.BillingEvent.OutputCostPer1M != 4 {
		t.Fatalf("unexpected catalog price: %+v", req.BillingEvent)
	}
}

func TestModelCatalogStrictPricingFailsClosed(t *testing.T) {
	module := NewBillingModuleWithSettings(true, Settings{
		Pricing:          PricingConfig{Currency: "USD"},
		ModelCatalogJSON: `{"version":"v1","unknown_model_policy":"deny","models":[{"provider":"known","model":"known","currency":"USD"}]}`,
	})
	err := module.Handle(context.Background(), &RequestContext{
		RequestID: "unknown-model", Request: openai.ChatCompletionRequest{Provider: "unknown", Model: "unknown"},
	})
	if err == nil {
		t.Fatal("strict pricing catalog accepted an unknown model")
	}
}

type pinnedPricingPolicy struct{}

func (pinnedPricingPolicy) Ready(context.Context) error { return nil }
func (pinnedPricingPolicy) Close()                      {}
func (pinnedPricingPolicy) Apply(_ context.Context, event *BillingEvent) error {
	if event.Phase == "commit" {
		event.CatalogVersion = "reserved-v1"
		event.PricingKey = "provider/model@reserved-v1"
		event.InputCostPer1M = 1
		event.OutputCostPer1M = 2
		event.Currency = "USD"
		event.Cost = pricingCost(event.InputTokens, event.OutputTokens, PricingSnapshot{InputCostPer1M: 1, OutputCostPer1M: 2})
	}
	return nil
}

func TestCommitCanUsePersistedPricingAfterCatalogRemoval(t *testing.T) {
	catalog, err := ParseModelCatalog(`{"version":"v2","unknown_model_policy":"deny","models":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	writer := &recordingUsageWriter{}
	module := BillingModule{
		required: true, pricing: PricingConfig{Currency: "USD"}, catalog: catalog,
		policy: pinnedPricingPolicy{}, writer: writer, lifecycle: NewLifecycleStore(),
	}
	req := RequestContext{
		RequestID: "removed-model", BillingPhase: "commit", PostResponse: true,
		Request: openai.ChatCompletionRequest{Provider: "provider", Model: "removed"},
		Usage:   &openai.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
	}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if len(writer.events) != 1 || writer.events[0].CatalogVersion != "reserved-v1" || math.Abs(writer.events[0].Cost-0.0002) > 1e-12 {
		t.Fatalf("persisted pricing was not applied: %+v", writer.events)
	}
}

func TestModelCatalogRejectsInvalidEntries(t *testing.T) {
	for _, raw := range []string{
		`{"models":[{"provider":"p","model":"m"}]}`,
		`{"version":"v1","unknown_model_policy":"free"}`,
		`{"version":"v1","models":[{"provider":"p","model":"m","output_cost_per_1m":-1}]}`,
		`{"version":"v1","models":[{"provider":"p","model":"m"},{"provider":"p","model":"m"}]}`,
	} {
		if _, err := ParseModelCatalog(raw); err == nil {
			t.Fatalf("invalid catalog was accepted: %s", raw)
		}
	}
}

func TestDocumentedModelCatalogExampleMatchesBillingSchema(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "docs", "model-catalog.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := ParseModelCatalog(string(raw))
	if err != nil || catalog.Version == "" || len(catalog.Models) != 2 {
		t.Fatalf("documented catalog does not match billing schema: catalog=%+v err=%v", catalog, err)
	}
}

func TestSuppliedRuntimePricingSnapshotOverridesStaticCatalog(t *testing.T) {
	req := &RequestContext{Metadata: map[string]string{"model_catalog.version": "runtime-v2", "model_catalog.pricing_key": "endpoint/model", "model_catalog.input_cost_per_1m": "2.5", "model_catalog.output_cost_per_1m": "5", "model_catalog.currency": "EUR"}}
	pricing, supplied, err := suppliedPricingSnapshot(req)
	if err != nil || !supplied {
		t.Fatalf("supplied=%v err=%v", supplied, err)
	}
	if pricing.CatalogVersion != "runtime-v2" || pricing.InputCostPer1M != 2.5 || pricing.Currency != "EUR" {
		t.Fatalf("pricing=%+v", pricing)
	}
}

func TestSuppliedRuntimePricingSnapshotFailsClosed(t *testing.T) {
	req := &RequestContext{Metadata: map[string]string{"model_catalog.version": "runtime-v2", "model_catalog.pricing_key": "endpoint/model", "model_catalog.input_cost_per_1m": "bad", "model_catalog.output_cost_per_1m": "5", "model_catalog.currency": "USD"}}
	if _, supplied, err := suppliedPricingSnapshot(req); !supplied || err == nil {
		t.Fatalf("supplied=%v err=%v", supplied, err)
	}
}
