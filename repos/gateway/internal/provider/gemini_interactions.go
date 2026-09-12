package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

func (g Gemini) Interactions(ctx context.Context, request openai.InteractionRequest) (openai.InteractionResponse, error) {
	if _, message := request.NativeResponseRequest(); message != "" || request.Stream || request.Background {
		return openai.InteractionResponse{}, geminiInvalid("interactions")
	}
	return g.doInteraction(ctx, request, false, nil)
}

func (g Gemini) StreamInteractions(ctx context.Context, request openai.InteractionRequest, write ResponseStreamWriter) (openai.InteractionResponse, error) {
	if _, message := request.NativeResponseRequest(); message != "" || !request.Stream || request.Background || write == nil {
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
		Model                 string                             `json:"model,omitempty"`
		Agent                 string                             `json:"agent,omitempty"`
		Input                 any                                `json:"input"`
		SystemInstruction     string                             `json:"system_instruction,omitempty"`
		Tools                 []openai.ResponseTool              `json:"tools,omitempty"`
		ResponseFormat        any                                `json:"response_format,omitempty"`
		PreviousInteractionID string                             `json:"previous_interaction_id,omitempty"`
		Store                 *bool                              `json:"store,omitempty"`
		Stream                bool                               `json:"stream,omitempty"`
		GenerationConfig      openai.InteractionGenerationConfig `json:"generation_config,omitempty"`
	}
	shared, _ := request.NativeResponseRequest()
	responseFormat := request.ResponseFormat
	if text, ok := shared.Text.(map[string]any); ok {
		responseFormat = text["format"]
	}
	agent := strings.TrimSpace(request.Agent)
	model := request.Model
	if agent != "" {
		model = ""
	}
	payload, err := json.Marshal(interactionPayload{
		Model: model, Agent: agent, Input: request.Input, SystemInstruction: request.SystemInstruction,
		Tools: request.Tools, ResponseFormat: responseFormat, PreviousInteractionID: request.PreviousInteractionID,
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
	return decodeGeminiInteraction(response.Body)
}

func (g Gemini) RetrieveInteraction(ctx context.Context, id string) (openai.InteractionResponse, error) {
	response, err := g.interactionResourceRequest(ctx, http.MethodGet, id, "")
	if err != nil {
		return openai.InteractionResponse{}, err
	}
	defer response.Body.Close()
	result, err := decodeGeminiInteraction(response.Body)
	if err == nil && result.ID != id {
		err = errors.New("Gemini returned a different interaction ID")
	}
	return result, err
}

func (g Gemini) CancelInteraction(ctx context.Context, id string) (openai.InteractionResponse, error) {
	response, err := g.interactionResourceRequest(ctx, http.MethodPost, id, ":cancel")
	if err != nil {
		return openai.InteractionResponse{}, err
	}
	defer response.Body.Close()
	var result struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := decodeBoundedJSON(response.Body, &result, maxResponseJSONBytes, "Gemini interaction cancellation"); err != nil {
		return openai.InteractionResponse{}, err
	}
	if result.ID != id || strings.TrimSpace(result.Status) == "" {
		return openai.InteractionResponse{}, errors.New("invalid Gemini interaction cancellation response")
	}
	return openai.InteractionResponse{ID: result.ID, Object: "interaction", Status: result.Status}, nil
}

func (g Gemini) DeleteInteraction(ctx context.Context, id string) error {
	response, err := g.interactionResourceRequest(ctx, http.MethodDelete, id, "")
	if err != nil {
		return err
	}
	return response.Body.Close()
}

func (g Gemini) interactionResourceRequest(ctx context.Context, method, id, suffix string) (*http.Response, error) {
	if !validResponseResourceID(id) {
		return nil, &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "interaction_id", Err: errors.New("invalid interaction ID")}
	}
	request, err := http.NewRequestWithContext(ctx, method, geminiBaseURL(g.baseURL)+"/interactions/"+url.PathEscape(id)+suffix, http.NoBody)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Api-Revision", "2026-05-20")
	if err := g.authorize(request); err != nil {
		return nil, err
	}
	response, err := g.client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		return nil, responseStatusError("gemini", response)
	}
	return response, nil
}

func decodeGeminiInteraction(body io.Reader) (openai.InteractionResponse, error) {
	var result openai.InteractionResponse
	if err := decodeBoundedJSON(body, &result, maxResponseJSONBytes, "Gemini interaction"); err != nil {
		return openai.InteractionResponse{}, err
	}
	if strings.TrimSpace(result.ID) == "" || strings.TrimSpace(result.Status) == "" {
		return openai.InteractionResponse{}, errors.New("invalid Gemini interaction response")
	}
	return result, nil
}

func decodeBoundedJSON(body io.Reader, target any, limit int64, label string) error {
	payload, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(payload)) > limit {
		return fmt.Errorf("%s response exceeds %d bytes", label, limit)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return err
	}
	return nil
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
