package modelcatalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Catalog struct {
	Version            string  `json:"version"`
	UnknownModelPolicy string  `json:"unknown_model_policy,omitempty"`
	Models             []Model `json:"models"`
	entries            map[string]Model
}

type Model struct {
	Provider        string   `json:"provider"`
	Model           string   `json:"model"`
	Capabilities    []string `json:"capabilities,omitempty"`
	MaxInputTokens  int      `json:"max_input_tokens,omitempty"`
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	InputCostPer1M  float64  `json:"input_cost_per_1m,omitempty"`
	OutputCostPer1M float64  `json:"output_cost_per_1m,omitempty"`
	Currency        string   `json:"currency,omitempty"`
}

func Parse(raw string) (Catalog, error) {
	if strings.TrimSpace(raw) == "" {
		return Catalog{UnknownModelPolicy: "legacy", entries: map[string]Model{}}, nil
	}
	var catalog Catalog
	if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
		return Catalog{}, errors.New("invalid model catalog JSON")
	}
	if len(catalog.Models) > 0 && strings.TrimSpace(catalog.Version) == "" {
		return Catalog{}, errors.New("model catalog version is required")
	}
	if catalog.UnknownModelPolicy == "" {
		catalog.UnknownModelPolicy = "legacy"
	}
	if catalog.UnknownModelPolicy != "legacy" && catalog.UnknownModelPolicy != "deny" {
		return Catalog{}, fmt.Errorf("invalid unknown_model_policy %q", catalog.UnknownModelPolicy)
	}
	catalog.entries = make(map[string]Model, len(catalog.Models))
	for index, model := range catalog.Models {
		model.Provider = strings.TrimSpace(model.Provider)
		model.Model = strings.TrimSpace(model.Model)
		if model.Provider == "" || model.Model == "" {
			return Catalog{}, fmt.Errorf("model catalog entry %d requires provider and model", index)
		}
		if model.MaxInputTokens < 0 || model.MaxOutputTokens < 0 || model.InputCostPer1M < 0 || model.OutputCostPer1M < 0 {
			return Catalog{}, fmt.Errorf("model catalog entry %s/%s contains a negative limit or price", model.Provider, model.Model)
		}
		key := catalogKey(model.Provider, model.Model)
		if _, exists := catalog.entries[key]; exists {
			return Catalog{}, fmt.Errorf("duplicate model catalog entry %s/%s", model.Provider, model.Model)
		}
		catalog.Models[index] = model
		catalog.entries[key] = model
	}
	return catalog, nil
}

func (c Catalog) Find(endpointName, endpointType string, models ...string) (Model, bool) {
	providers := uniqueNonEmpty(endpointName, endpointType, "*")
	modelNames := uniqueNonEmpty(models...)
	modelNames = append(modelNames, "*")
	for _, provider := range providers {
		for _, model := range modelNames {
			if entry, found := c.entries[catalogKey(provider, model)]; found {
				return entry, true
			}
		}
	}
	return Model{}, false
}

func (c Catalog) DenyUnknownModels() bool { return c.UnknownModelPolicy == "deny" }

func catalogKey(provider, model string) string { return provider + "\x00" + model }

func uniqueNonEmpty(values ...string) []string {
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
