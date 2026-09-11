package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

func (g Gemini) Interactions(ctx context.Context, request openai.InteractionRequest) (openai.InteractionResponse, error) {
	if _, message := request.NativeResponseRequest(); message != "" || request.Stream || request.Background || request.PreviousInteractionID != "" || request.Store != nil && *request.Store {
		return openai.InteractionResponse{}, geminiInvalid("interactions")
	}
	return g.doInteraction(ctx, request, false, nil)
}

func (g Gemini) StreamInteractions(ctx context.Context, request openai.InteractionRequest, write ResponseStreamWriter) (openai.InteractionResponse, error) {
	if _, message := request.NativeResponseRequest(); message != "" || !request.Stream || request.Background || request.PreviousInteractionID != "" || request.Store != nil && *request.Store || write == nil {
		return openai.InteractionResponse{}, geminiInvalid("interactions stream")
	}
	return g.doInteraction(ctx, request, true, write)
}

func (g Gemini) doInteraction(ctx context.Context, request openai.InteractionRequest, stream bool, write ResponseStreamWriter) (openai.InteractionResponse, error) {
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
		Stream                bool                               `json:"stream,omitempty"`
		GenerationConfig      openai.InteractionGenerationConfig `json:"generation_config,omitempty"`
	}
	payload, err := json.Marshal(interactionPayload{
		Model: request.Model, Input: request.Input, SystemInstruction: request.SystemInstruction,
		Tools: request.Tools, ResponseFormat: request.ResponseFormat, PreviousInteractionID: request.PreviousInteractionID,
		Store: request.Store, Stream: request.Stream, GenerationConfig: request.GenerationConfig,
	})
	if err != nil {
		return openai.InteractionResponse{}, err
	}
	endpoint := geminiBaseURL(g.baseURL) + "/interactions"
	if stream {
		endpoint += "?alt=sse"
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
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
	if stream {
		if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
			return openai.InteractionResponse{}, errors.New("Gemini interaction stream returned non-SSE content")
		}
		return readGeminiInteractionStream(response.Body, write)
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

func readGeminiInteractionStream(body io.Reader, write ResponseStreamWriter) (openai.InteractionResponse, error) {
	var result openai.InteractionResponse
	completed := false
	err := scanSSEData(&responseStreamReader{source: body, remaining: maxResponseStreamBytes}, func(payload string) error {
		if payload == "[DONE]" {
			return nil
		}
		var envelope struct {
			EventType   string                     `json:"event_type"`
			Interaction openai.InteractionResponse `json:"interaction"`
		}
		if err := json.Unmarshal([]byte(payload), &envelope); err != nil || strings.TrimSpace(envelope.EventType) == "" {
			return errors.New("invalid Gemini interaction stream event")
		}
		if envelope.EventType == "interaction.completed" {
			if completed || strings.TrimSpace(envelope.Interaction.ID) == "" || strings.TrimSpace(envelope.Interaction.Status) == "" {
				return errors.New("invalid Gemini interaction completion event")
			}
			completed = true
			result = envelope.Interaction
			return nil
		}
		return write(envelope.EventType, payload)
	})
	if err != nil {
		return openai.InteractionResponse{}, err
	}
	if !completed {
		return openai.InteractionResponse{}, errors.New("Gemini interaction stream ended without a completion event")
	}
	return result, nil
}
