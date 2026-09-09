package modelcatalog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogLookupPrecedenceAndValidation(t *testing.T) {
	catalog, err := Parse(`{
		"version":"2026-08-21.1","unknown_model_policy":"deny","models":[
			{"provider":"*","model":"*","capabilities":["chat"]},
			{"provider":"openai","model":"gpt-test","capabilities":["chat","tools"]},
			{"provider":"endpoint-a","model":"gpt-test","capabilities":["responses"]}
		]}`)
	if err != nil {
		t.Fatal(err)
	}
	entry, found := catalog.Find("endpoint-a", "openai", "gpt-test")
	if !found || len(entry.Capabilities) != 1 || entry.Capabilities[0] != "responses" {
		t.Fatalf("endpoint-specific entry did not win: %+v found=%v", entry, found)
	}
	entry, found = catalog.Find("endpoint-b", "openai", "gpt-test")
	if !found || len(entry.Capabilities) != 2 {
		t.Fatalf("provider-type entry did not match: %+v found=%v", entry, found)
	}
	if !catalog.DenyUnknownModels() {
		t.Fatal("deny policy was not parsed")
	}
}

func TestCatalogLookupSupportsManagedProviderIdentity(t *testing.T) {
	catalog, err := Parse(`{"version":"v1","models":[{"provider":"azure-open-ai","model":"gpt-5.6-luna","input_cost_per_1m":0.2,"currency":"USD"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	entry, found := catalog.FindForProviders([]string{"luna-deployment", "azure-open-ai", "openai-compatible"}, "gpt-5.6-luna")
	if !found || entry.Provider != "azure-open-ai" || entry.InputCostPer1M != 0.2 {
		t.Fatalf("managed provider entry was not resolved: %+v found=%v", entry, found)
	}
}

func TestCatalogRejectsInvalidOrDuplicateEntries(t *testing.T) {
	for _, raw := range []string{
		`{"models":[{"provider":"p","model":"m"}]}`,
		`{"version":"v1","unknown_model_policy":"free"}`,
		`{"version":"v1","models":[{"provider":"p","model":"m","input_cost_per_1m":-1}]}`,
		`{"version":"v1","models":[{"provider":"p","model":"m","search_cost_per_1k":-1}]}`,
		`{"version":"v1","models":[{"provider":"p","model":"m"},{"provider":"p","model":"m"}]}`,
	} {
		if _, err := Parse(raw); err == nil {
			t.Fatalf("invalid catalog was accepted: %s", raw)
		}
	}
}

func TestDocumentedCatalogExampleParses(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "docs", "model-catalog.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Parse(string(raw))
	if err != nil || catalog.Version == "" || len(catalog.Models) != 2 {
		t.Fatalf("documented catalog does not match gateway schema: catalog=%+v err=%v", catalog, err)
	}
}
