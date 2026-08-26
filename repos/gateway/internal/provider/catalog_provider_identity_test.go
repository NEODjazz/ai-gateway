package provider

import (
	"context"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modelcatalog"
	"ai-gateway-gateway/internal/modules"
)

func TestManagedProviderIdentityOwnsModelAndAppliesCatalogPricing(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","models":[{"provider":"azure-open-ai","model":"gpt-5.6-luna","input_cost_per_1m":0.2,"output_cost_per_1m":20,"currency":"USD"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := Endpoint{Name: "luna-deployment", ProviderID: "azure-open-ai", Type: "openai-compatible", Models: []string{"gpt-5.6-luna"}, Provider: Demo{}}
	if metadata := providerMetadata(endpoint); metadata["provider.id"] != "azure-open-ai" {
		t.Fatalf("managed provider identity missing from metadata: %+v", metadata)
	}
	router := Router{catalog: modelcatalog.NewRegistry(catalog, nil, time.Second), endpoints: []Endpoint{endpoint}}

	models := router.Models()
	if len(models) != 1 || models[0].OwnedBy != "azure-open-ai" {
		t.Fatalf("unexpected public model identity: %+v", models)
	}
	req := modules.RequestContext{}
	router.applyCatalogPricing(context.Background(), &req, endpoint, "gpt-5.6-luna")
	if req.Metadata["model_catalog.pricing_key"] != "azure-open-ai/gpt-5.6-luna" || req.Metadata["model_catalog.output_cost_per_1m"] != "20" {
		t.Fatalf("provider pricing was not applied: %+v", req.Metadata)
	}
}
