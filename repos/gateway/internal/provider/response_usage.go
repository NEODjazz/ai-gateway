package provider

import (
	"encoding/json"
	"errors"

	"ai-gateway-gateway/internal/openai"
)

func validateResponseUsage(usage openai.ResponseUsage) error {
	if hasNegativeCompletionTokenDetails(usage.OutputTokensDetails) {
		return errors.New("invalid negative Responses output token details")
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.TotalTokens < 0 {
		return errors.New("invalid negative Responses token usage")
	}
	if usage.InputTokens > int(^uint(0)>>1)-usage.OutputTokens {
		return errors.New("Responses token usage exceeds integer range")
	}
	if details := usage.InputTokensDetails; details != nil {
		if details.CachedTokens < 0 || details.CacheWriteTokens < 0 || details.CacheCreationTokens < 0 || details.AudioTokens < 0 || details.ImageTokens < 0 || details.ReasoningTokens < 0 || details.TextTokens < 0 {
			return errors.New("invalid negative Responses input token details")
		}
	}
	return nil
}

// Preserve a count already reported by an earlier SSE response snapshot when a
// later partial snapshot omits usage or input_tokens.
func recordResponseInputUsage(payload []byte, response *openai.ResponseResponse) error {
	var wire struct {
		Usage *struct {
			InputTokens *int `json:"input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		return err
	}
	if wire.Usage != nil && wire.Usage.InputTokens != nil {
		response.InputTokensReported = true
	}
	return nil
}
