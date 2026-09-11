package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

func (g Gemini) Interactions(ctx context.Context, request openai.InteractionRequest) (openai.InteractionResponse, error) {
	if _, message := request.NativeResponseRequest(); message != "" || request.Stream || request.Background || request.PreviousInteractionID != "" || request.Store != nil && *request.Store {
		return openai.InteractionResponse{}, geminiInvalid("interactions")
	}
	base, err := url.Parse(g.baseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return openai.InteractionResponse{}, errors.New("invalid Gemini base URL")
	}
	type interactionPayload struct {
		Model                 string                             `json:"model"`
		Input                 any                                `json:"input"`
		SystemInstruction     string                             `json:"system_instruction,omitempty"`
		Tools                 []openai.ResponseTool              `json:"tools,omitempty"`
		ResponseFormat        any                                `json:"response_format,omitempty"`
		PreviousInteractionID string                             `json:"previous_interaction_id,omitempty"`
		Store                 *bool                              `json:"store,omitempty"`
		GenerationConfig      openai.InteractionGenerationConfig `json:"generation_config,omitempty"`
	}
	payload, err := json.Marshal(interactionPayload{
		Model: request.Model, Input: request.Input, SystemInstruction: request.SystemInstruction,
		Tools: request.Tools, ResponseFormat: request.ResponseFormat, PreviousInteractionID: request.PreviousInteractionID,
		Store: request.Store, GenerationConfig: request.GenerationConfig,
	})
	if err != nil {
		return openai.InteractionResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiBaseURL(g.baseURL)+"/interactions", bytes.NewReader(payload))
	if err != nil {
		return openai.InteractionResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Api-Revision", "2026-05-20")
	if err := g.authorize(httpRequest); err != nil {
		return openai.InteractionResponse{}, err
	}
	response, err := g.client.Do(httpRequest)
	if err != nil {
		return openai.InteractionResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.InteractionResponse{}, responseStatusError("gemini", response)
	}
	var result openai.InteractionResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return openai.InteractionResponse{}, err
	}
	if strings.TrimSpace(result.ID) == "" || strings.TrimSpace(result.Status) == "" {
		return openai.InteractionResponse{}, errors.New("invalid Gemini interaction response")
	}
	return result, nil
}
