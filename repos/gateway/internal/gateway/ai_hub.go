package gateway

import (
	"net/http"
	"sort"
	"strings"

	"ai-gateway-gateway/internal/provider"
)

type AIHubModel struct {
	Provider           string   `json:"provider"`
	Model              string   `json:"model"`
	Capabilities       []string `json:"capabilities,omitempty"`
	MaxInputTokens     int      `json:"max_input_tokens,omitempty"`
	MaxOutputTokens    int      `json:"max_output_tokens,omitempty"`
	InputCostPer1M     float64  `json:"input_cost_per_1m,omitempty"`
	OutputCostPer1M    float64  `json:"output_cost_per_1m,omitempty"`
	TrainingCostPer1M  float64  `json:"training_cost_per_1m,omitempty"`
	SearchCostPer1K    float64  `json:"search_cost_per_1k,omitempty"`
	CharacterCostPer1M float64  `json:"character_cost_per_1m,omitempty"`
	PageCostPer1K      float64  `json:"page_cost_per_1k,omitempty"`
	AudioCostPerMinute float64  `json:"audio_cost_per_minute,omitempty"`
	VideoCostPerSecond float64  `json:"video_cost_per_second,omitempty"`
	ImageCostPerUnit   float64  `json:"image_cost_per_unit,omitempty"`
	Currency           string   `json:"currency,omitempty"`
	Deployments        []string `json:"deployments"`
	Available          bool     `json:"available"`
}

type CostRecommendation struct {
	Type                    string  `json:"type"`
	Model                   string  `json:"model"`
	CurrentProvider         string  `json:"current_provider,omitempty"`
	RecommendedProvider     string  `json:"recommended_provider,omitempty"`
	Currency                string  `json:"currency,omitempty"`
	EstimatedSavingsPercent float64 `json:"estimated_savings_percent,omitempty"`
	Summary                 string  `json:"summary"`
}

func (h Handler) GetAIHubModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.modelRegistryAdmin(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": h.aiHubModels(r)})
}

func (h Handler) GetCostRecommendations(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.modelRegistryAdmin(w, r); !ok {
		return
	}
	models := h.aiHubModels(r)
	recommendations := make([]CostRecommendation, 0)
	for _, item := range models {
		if !item.Available {
			recommendations = append(recommendations, CostRecommendation{Type: "unavailable", Model: item.Model, CurrentProvider: item.Provider, Summary: "Catalog entry has no available runtime deployment"})
		}
		if item.Available && item.Currency == "" {
			recommendations = append(recommendations, CostRecommendation{Type: "missing_pricing", Model: item.Model, CurrentProvider: item.Provider, Summary: "Add pricing metadata before cost-based routing"})
		}
	}
	for _, current := range models {
		currentCost := current.InputCostPer1M + current.OutputCostPer1M
		if !current.Available || current.Currency == "" || currentCost <= 0 {
			continue
		}
		var best *AIHubModel
		for index := range models {
			candidate := &models[index]
			candidateCost := candidate.InputCostPer1M + candidate.OutputCostPer1M
			if candidate.Model == current.Model && candidate.Provider != current.Provider && candidate.Available && candidate.Currency == current.Currency && candidateCost > 0 && candidateCost < currentCost && (best == nil || candidateCost < best.InputCostPer1M+best.OutputCostPer1M) {
				best = candidate
			}
		}
		if best != nil {
			bestCost := best.InputCostPer1M + best.OutputCostPer1M
			recommendations = append(recommendations, CostRecommendation{Type: "cheaper_alternative", Model: current.Model, CurrentProvider: current.Provider, RecommendedProvider: best.Provider, Currency: current.Currency, EstimatedSavingsPercent: (currentCost - bestCost) / currentCost * 100, Summary: "A lower catalog price is available for the same model"})
		}
	}
	sort.Slice(recommendations, func(i, j int) bool {
		if recommendations[i].Type == recommendations[j].Type {
			if recommendations[i].Model == recommendations[j].Model {
				return recommendations[i].CurrentProvider < recommendations[j].CurrentProvider
			}
			return recommendations[i].Model < recommendations[j].Model
		}
		return recommendations[i].Type < recommendations[j].Type
	})
	writeJSON(w, http.StatusOK, map[string]any{"data": recommendations, "basis": "catalog_prices_and_runtime_availability", "usage_projection": false})
}

func (h Handler) aiHubModels(r *http.Request) []AIHubModel {
	catalog := h.models.Current(r.Context())
	deployments := []provider.ModelDeployment{}
	if controller, ok := h.deploymentController(); ok {
		deployments = controller.ListModelDeployments(r.Context())
	}
	result := make([]AIHubModel, 0, len(catalog.Models))
	for _, entry := range catalog.Models {
		item := AIHubModel{Provider: entry.Provider, Model: entry.Model, Capabilities: append([]string(nil), entry.Capabilities...), MaxInputTokens: entry.MaxInputTokens, MaxOutputTokens: entry.MaxOutputTokens, InputCostPer1M: entry.InputCostPer1M, OutputCostPer1M: entry.OutputCostPer1M, TrainingCostPer1M: entry.TrainingCostPer1M, SearchCostPer1K: entry.SearchCostPer1K, CharacterCostPer1M: entry.CharacterCostPer1M, PageCostPer1K: entry.PageCostPer1K, AudioCostPerMinute: entry.AudioCostPerMinute, VideoCostPerSecond: entry.VideoCostPerSecond, ImageCostPerUnit: entry.ImageCostPerUnit, Currency: entry.Currency, Deployments: []string{}}
		for _, deployment := range deployments {
			if deployment.ProviderID != entry.Provider && deployment.ProviderType != entry.Provider && deployment.ID != entry.Provider {
				continue
			}
			if !containsModel(deployment.Models, entry.Model) {
				continue
			}
			item.Deployments = append(item.Deployments, deployment.ID)
			if deployment.Enabled && deployment.RuntimeState == "available" {
				item.Available = true
			}
		}
		sort.Strings(item.Deployments)
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Model == result[j].Model {
			return result[i].Provider < result[j].Provider
		}
		return result[i].Model < result[j].Model
	})
	return result
}

func containsModel(models []string, wanted string) bool {
	for _, model := range models {
		if model == wanted || model == "*" || (strings.HasSuffix(model, "*") && strings.HasPrefix(wanted, strings.TrimSuffix(model, "*"))) {
			return true
		}
	}
	return false
}
