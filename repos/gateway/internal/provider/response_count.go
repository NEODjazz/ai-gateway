package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

var ErrResponseInputTokenCountUnsupported = errors.New("response input token counting is not supported by the selected deployment")

func (p OpenAICompatible) CountResponseInputTokens(ctx context.Context, request openai.ResponseInputTokenCountRequest) (openai.ResponseInputTokenCount, error) {
	if strings.TrimSpace(request.Model) == "" || request.Input == nil {
		return openai.ResponseInputTokenCount{}, &Error{Class: FailureClientRequest, Provider: p.providerName(), StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New("model and input are required")}
	}
	body, err := json.Marshal(struct {
		Model             string                    `json:"model"`
		Input             any                       `json:"input"`
		Instructions      string                    `json:"instructions,omitempty"`
		Tools             []openai.ResponseTool     `json:"tools,omitempty"`
		ToolChoice        any                       `json:"tool_choice,omitempty"`
		ParallelToolCalls *bool                     `json:"parallel_tool_calls,omitempty"`
		Text              any                       `json:"text,omitempty"`
		PreviousResponse  string                    `json:"previous_response_id,omitempty"`
		Reasoning         *openai.ResponseReasoning `json:"reasoning,omitempty"`
		Truncation        *string                   `json:"truncation,omitempty"`
	}{
		Model: request.Model, Input: request.Input, Instructions: request.Instructions,
		Tools: request.Tools, ToolChoice: request.ToolChoice, ParallelToolCalls: request.ParallelToolCalls,
		Text: request.Text, PreviousResponse: request.PreviousResponse, Reasoning: request.Reasoning,
		Truncation: request.Truncation,
	})
	if err != nil {
		return openai.ResponseInputTokenCount{}, err
	}
	if len(body) > openai.MaxInferenceBodyBytes {
		return openai.ResponseInputTokenCount{}, errors.New("response token-count request exceeds inference limit")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "responses/input_tokens"), bytes.NewReader(body))
	if err != nil {
		return openai.ResponseInputTokenCount{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	client := *p.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(httpReq)
	if err != nil {
		return openai.ResponseInputTokenCount{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return openai.ResponseInputTokenCount{}, responseStatusError(p.providerName(), response)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil {
		return openai.ResponseInputTokenCount{}, err
	}
	if len(payload) > 64<<10 {
		return openai.ResponseInputTokenCount{}, errors.New("response token-count result exceeds limit")
	}
	var decoded struct {
		Object      string `json:"object"`
		InputTokens *int   `json:"input_tokens"`
	}
	if json.Unmarshal(payload, &decoded) != nil || decoded.Object != "response.input_tokens" || decoded.InputTokens == nil || *decoded.InputTokens < 0 {
		return openai.ResponseInputTokenCount{}, errors.New("invalid provider response token count")
	}
	return openai.ResponseInputTokenCount{Object: decoded.Object, InputTokens: *decoded.InputTokens}, nil
}

func (r Router) CountResponseInputTokens(ctx context.Context, req modules.RequestContext) (openai.ResponseInputTokenCount, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if req.ResponseRequest == nil {
		return openai.ResponseInputTokenCount{}, errors.New("missing response token-count request")
	}
	request := *req.ResponseRequest
	var candidates []Endpoint
	if request.PreviousResponse != "" {
		_, endpoint, err := r.responseResource(ctx, req, request.PreviousResponse)
		if err != nil {
			return openai.ResponseInputTokenCount{}, err
		}
		if !endpoint.supportsModel(request.Model) || !endpoint.supportsCapabilities(requiredResponseCapabilities(request, false)...) {
			return openai.ResponseInputTokenCount{}, ErrResponseDeploymentChanged
		}
		candidates = []Endpoint{endpoint}
	} else {
		var err error
		candidates, err = r.responseCandidates(ctx, req, request, requiredResponseCapabilities(request, false)...)
		if err != nil {
			return openai.ResponseInputTokenCount{}, err
		}
	}
	available := candidates[:0]
	for _, endpoint := range candidates {
		if _, ok := endpoint.Provider.(ResponseInputTokenCountClient); ok {
			available = append(available, endpoint)
		}
	}
	if len(available) == 0 {
		return openai.ResponseInputTokenCount{}, ErrResponseInputTokenCountUnsupported
	}
	endpoint := available[0]
	if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
		return openai.ResponseInputTokenCount{}, modules.ErrGuardrailUnavailable
	}
	attempt := providerAttemptContext(req, endpoint)
	if err := r.modules.RunTokenCount(ctx, &attempt); err != nil {
		return openai.ResponseInputTokenCount{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
	}
	if attempt.ResponseRequest == nil {
		return openai.ResponseInputTokenCount{}, errors.New("module removed response token-count request")
	}
	effective := attempt.ResponseRequest
	countRequest := openai.ResponseInputTokenCountRequest{
		Provider: effective.Provider, Model: effective.Model, Input: effective.Input,
		Instructions: effective.Instructions, Tools: effective.Tools, ToolChoice: effective.ToolChoice,
		ParallelToolCalls: effective.ParallelToolCalls, Text: effective.Text,
		PreviousResponse: effective.PreviousResponse, Reasoning: effective.Reasoning, Truncation: effective.Truncation,
	}
	release, err := endpoint.Admission.acquire(ctx, endpoint.Name)
	if err != nil {
		return openai.ResponseInputTokenCount{}, err
	}
	defer release()
	started := time.Now()
	result, err := endpoint.Provider.(ResponseInputTokenCountClient).CountResponseInputTokens(ctx, countRequest)
	if r.observer != nil {
		outcome := "ok"
		if err != nil {
			outcome = "error"
		}
		r.observer.ObserveProvider(endpoint.Name, endpoint.Type, "responses_input_tokens", outcome, time.Since(started))
	}
	return result, err
}
