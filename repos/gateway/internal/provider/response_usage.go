package provider

import (
	"encoding/json"
	"errors"

	"ai-gateway-gateway/internal/openai"
)

func validateResponseUsage(usage openai.ResponseUsage) error {
	if details := usage.OutputTokensDetails; details != nil && details.ReasoningTokens < 0 {
		return errors.New("invalid negative Responses reasoning token usage")
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.TotalTokens < 0 {
		return errors.New("invalid negative Responses token usage")
	}
	if usage.InputTokens > int(^uint(0)>>1)-usage.OutputTokens {
		return errors.New("Responses token usage exceeds integer range")
	}
	if details := usage.InputTokensDetails; details != nil {
		if details.CachedTokens < 0 || details.CacheWriteTokens < 0 || details.CacheCreationTokens < 0 {
			return errors.New("invalid negative Responses cache token usage")
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
