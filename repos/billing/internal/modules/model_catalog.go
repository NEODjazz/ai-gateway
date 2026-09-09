package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type ModelCatalog struct {
	Version            string              `json:"version"`
	UnknownModelPolicy string              `json:"unknown_model_policy,omitempty"`
	Models             []ModelCatalogEntry `json:"models,omitempty"`
	entries            map[string]ModelCatalogEntry
}

type ModelCatalogEntry struct {
	Provider           string  `json:"provider"`
	Model              string  `json:"model"`
	InputCostPer1M     float64 `json:"input_cost_per_1m,omitempty"`
	OutputCostPer1M    float64 `json:"output_cost_per_1m,omitempty"`
	SearchCostPer1K    float64 `json:"search_cost_per_1k,omitempty"`
	CharacterCostPer1M float64 `json:"character_cost_per_1m,omitempty"`
	Currency           string  `json:"currency,omitempty"`
}

type PricingSnapshot struct {
	CatalogVersion     string
	PricingKey         string
	InputCostPer1M     float64
	OutputCostPer1M    float64
	SearchCostPer1K    float64
	CharacterCostPer1M float64
	Currency           string
}

func ParseModelCatalog(raw string) (ModelCatalog, error) {
	if strings.TrimSpace(raw) == "" {
		return ModelCatalog{UnknownModelPolicy: "legacy", entries: map[string]ModelCatalogEntry{}}, nil
	}
	var catalog ModelCatalog
	if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
		return ModelCatalog{}, errors.New("invalid model catalog JSON")
	}
	if len(catalog.Models) > 0 && strings.TrimSpace(catalog.Version) == "" {
		return ModelCatalog{}, errors.New("model catalog version is required")
	}
	if catalog.UnknownModelPolicy == "" {
		catalog.UnknownModelPolicy = "legacy"
	}
	if catalog.UnknownModelPolicy != "legacy" && catalog.UnknownModelPolicy != "deny" {
		return ModelCatalog{}, fmt.Errorf("invalid unknown_model_policy %q", catalog.UnknownModelPolicy)
	}
	catalog.entries = make(map[string]ModelCatalogEntry, len(catalog.Models))
	for index, entry := range catalog.Models {
		entry.Provider = strings.TrimSpace(entry.Provider)
		entry.Model = strings.TrimSpace(entry.Model)
		if entry.Provider == "" || entry.Model == "" {
			return ModelCatalog{}, fmt.Errorf("model catalog entry %d requires provider and model", index)
		}
		if entry.InputCostPer1M < 0 || entry.OutputCostPer1M < 0 || entry.SearchCostPer1K < 0 || entry.CharacterCostPer1M < 0 {
			return ModelCatalog{}, fmt.Errorf("model catalog entry %s/%s contains a negative price", entry.Provider, entry.Model)
		}
		key := modelCatalogKey(entry.Provider, entry.Model)
		if _, exists := catalog.entries[key]; exists {
			return ModelCatalog{}, fmt.Errorf("duplicate model catalog entry %s/%s", entry.Provider, entry.Model)
		}
		catalog.Models[index] = entry
		catalog.entries[key] = entry
	}
	return catalog, nil
}

func (c ModelCatalog) Resolve(endpointName, endpointType, provider, model string, legacy PricingConfig) (PricingSnapshot, error) {
	providers := uniqueCatalogValues(endpointName, endpointType, provider, "*")
	models := uniqueCatalogValues(model, "*")
	for _, candidateProvider := range providers {
		for _, candidateModel := range models {
			if entry, found := c.entries[modelCatalogKey(candidateProvider, candidateModel)]; found {
				currency := entry.Currency
				if currency == "" {
					currency = legacy.Currency
				}
				return PricingSnapshot{
					CatalogVersion: c.Version, PricingKey: candidateProvider + "/" + candidateModel,
					InputCostPer1M: entry.InputCostPer1M, OutputCostPer1M: entry.OutputCostPer1M, SearchCostPer1K: entry.SearchCostPer1K, CharacterCostPer1M: entry.CharacterCostPer1M, Currency: currency,
				}, nil
			}
		}
	}
	if c.UnknownModelPolicy == "deny" {
		return PricingSnapshot{}, fmt.Errorf("no catalog pricing for provider=%q model=%q", provider, model)
	}
	return PricingSnapshot{
		CatalogVersion: "legacy-env", PricingKey: "legacy-env",
		InputCostPer1M: legacy.InputPricePer1K * 1000, OutputCostPer1M: legacy.OutputPricePer1K * 1000, Currency: legacy.Currency,
	}, nil
}

func pricingCost(inputTokens, outputTokens, inputCharacters, searchRequests int, pricing PricingSnapshot) float64 {
	return (float64(inputTokens)/1_000_000)*pricing.InputCostPer1M + (float64(outputTokens)/1_000_000)*pricing.OutputCostPer1M + (float64(inputCharacters)/1_000_000)*pricing.CharacterCostPer1M + (float64(searchRequests)/1_000)*pricing.SearchCostPer1K
}

func modelCatalogKey(provider, model string) string { return provider + "\x00" + model }

func uniqueCatalogValues(values ...string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
