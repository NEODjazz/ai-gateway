package provider

import (
	"encoding/json"
	"errors"
	"fmt"

	"ai-gateway-gateway/internal/openai"
)

func validateResponseUsage(usage openai.ResponseUsage) error {
	if hasNegativeCompletionTokenDetails(usage.OutputTokensDetails) {
		return errors.New("invalid negative Responses output token details")
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.TotalTokens < 0 {
		return errors.New("invalid negative Responses token usage")
	}
	if usage.NumSourcesUsed != nil && *usage.NumSourcesUsed < 0 || usage.NumServerSideToolsUsed != nil && *usage.NumServerSideToolsUsed < 0 {
		return errors.New("invalid negative Responses provider usage counters")
	}
	if details := usage.ServerSideToolUsageDetails; details != nil {
		if details.XPostsFetched != nil && *details.XPostsFetched < 0 || details.XUsersFetched != nil && *details.XUsersFetched < 0 {
			return errors.New("invalid negative Responses server-side tool usage details")
		}
	}
	if usage.InputTokens > int(^uint(0)>>1)-usage.OutputTokens {
		return errors.New("Responses token usage exceeds integer range")
	}
	if details := usage.InputTokensDetails; details != nil {
		if details.CachedTokens < 0 || details.CacheWriteTokens < 0 || details.CacheCreationTokens < 0 || details.AudioTokens < 0 || details.ImageTokens < 0 || details.ReasoningTokens < 0 || details.TextTokens < 0 {
			return errors.New("invalid negative Responses input token details")
		}
		if details.CachedTokens > usage.InputTokens || details.CacheWriteTokens > usage.InputTokens-details.CachedTokens || details.CacheCreationTokens > usage.InputTokens-details.CachedTokens {
			return errors.New("Responses cache token details exceed input usage")
		}
	}
	return nil
}

func validateReportedResponseTotal(response openai.ResponseResponse) error {
	if response.InputTokensReported && response.OutputTokensReported && response.TotalTokensReported &&
		response.Usage.TotalTokens != response.Usage.InputTokens+response.Usage.OutputTokens {
		return errors.New("Responses total token usage is inconsistent")
	}
	return nil
}

func validateExactResponseUsage(response openai.ResponseResponse, providerName string) error {
	if response.Status != "" && response.Status != "completed" && response.Status != "incomplete" {
		return nil
	}
	if !response.InputTokensReported || !response.OutputTokensReported || !response.TotalTokensReported ||
		response.Usage.TotalTokens != response.Usage.InputTokens+response.Usage.OutputTokens {
		return fmt.Errorf("%s Responses requires exact input, output and total token usage", providerName)
	}
	return nil
}

func validateExactResponseTerminalUsage(payload, providerName string) error {
	var event struct {
		Response struct {
			Usage *struct {
				InputTokens  *int `json:"input_tokens"`
				OutputTokens *int `json:"output_tokens"`
				TotalTokens  *int `json:"total_tokens"`
			} `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return err
	}
	if event.Response.Usage == nil || event.Response.Usage.InputTokens == nil ||
		event.Response.Usage.OutputTokens == nil || event.Response.Usage.TotalTokens == nil {
		return fmt.Errorf("%s Responses terminal event requires exact token usage", providerName)
	}
	if *event.Response.Usage.TotalTokens != *event.Response.Usage.InputTokens+*event.Response.Usage.OutputTokens {
		return fmt.Errorf("%s Responses terminal event has inconsistent token usage", providerName)
	}
	return nil
}

// Preserve a count already reported by an earlier SSE response snapshot when a
// later partial snapshot omits usage or input_tokens.
func recordResponseInputUsage(payload []byte, response *openai.ResponseResponse) error {
	var wire struct {
		Usage *struct {
			InputTokens  *int `json:"input_tokens"`
			OutputTokens *int `json:"output_tokens"`
			TotalTokens  *int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		return err
	}
	if wire.Usage != nil {
		response.InputTokensReported = response.InputTokensReported || wire.Usage.InputTokens != nil
		response.OutputTokensReported = response.OutputTokensReported || wire.Usage.OutputTokens != nil
		response.TotalTokensReported = response.TotalTokensReported || wire.Usage.TotalTokens != nil
	}
	return nil
}
