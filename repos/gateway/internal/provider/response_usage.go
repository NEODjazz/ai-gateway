package provider

import (
	"errors"

	"ai-gateway-gateway/internal/openai"
)

func validateResponseUsage(usage openai.ResponseUsage) error {
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
