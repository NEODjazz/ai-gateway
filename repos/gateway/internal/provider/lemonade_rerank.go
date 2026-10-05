package provider

import (
	"encoding/json"
	"errors"
	"io"

	"ai-gateway-gateway/internal/openai"
)

func decodeLemonadeRerank(reader io.Reader) (openai.RerankResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, (8<<20)+1))
	if err != nil {
		return openai.RerankResponse{}, err
	}
	if len(payload) > 8<<20 {
		return openai.RerankResponse{}, errors.New("Lemonade rerank response exceeds limit")
	}
	var wire struct {
		ID      string                `json:"id"`
		Results []openai.RerankResult `json:"results"`
		Usage   *struct {
			PromptTokens *int `json:"prompt_tokens"`
			TotalTokens  *int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		return openai.RerankResponse{}, err
	}
	response := openai.RerankResponse{ID: wire.ID, Results: wire.Results}
	if wire.Usage != nil {
		if wire.Usage.PromptTokens == nil || wire.Usage.TotalTokens == nil || *wire.Usage.PromptTokens < 0 || *wire.Usage.PromptTokens != *wire.Usage.TotalTokens {
			return openai.RerankResponse{}, errors.New("Lemonade returned invalid rerank usage")
		}
		response.Meta = &openai.RerankResponseMeta{Tokens: &openai.RerankTokens{InputTokens: *wire.Usage.PromptTokens}, BilledUnits: &openai.RerankBilledUnits{TotalTokens: *wire.Usage.TotalTokens}}
		response.UsageReported = true
	}
	return response, nil
}
