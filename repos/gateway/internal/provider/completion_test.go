package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type completionTestClient struct {
	calls   int
	request openai.CompletionRequest
	result  openai.CompletionResponse
}

func (*completionTestClient) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}
func (*completionTestClient) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, nil
}
func (c *completionTestClient) Completions(_ context.Context, request openai.CompletionRequest) (openai.CompletionResponse, error) {
	c.calls++
	c.request = request
	return c.result, nil
}

type completionLifecycleModule struct {
	pre, post int
	response  *openai.CompletionResponse
}

func (*completionLifecycleModule) Name() string   { return "completion-test" }
func (*completionLifecycleModule) Required() bool { return true }
func (m *completionLifecycleModule) Handle(_ context.Context, req *modules.RequestContext) error {
	m.pre++
	req.Request.Messages[0].Content = "masked"
	return nil
}
func (*completionLifecycleModule) PostResponseEnabled() bool { return true }
func (m *completionLifecycleModule) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	m.post++
	m.response = req.CompletionResponse
	return nil
}

func validCompletionResponse() openai.CompletionResponse {
	return openai.CompletionResponse{
		ID: "cmpl_1", Object: "text_completion", Created: 7, Model: "model",
		Choices: []openai.CompletionChoice{{Index: 0, Text: "done", FinishReason: "stop"}},
		Usage:   openai.Usage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6},
	}
}

func TestRouterCompletionsRunsProviderLifecycleWithEffectivePrompt(t *testing.T) {
	client := &completionTestClient{result: validCompletionResponse()}
	module := &completionLifecycleModule{}
	router := Router{
		endpoints: []Endpoint{{Name: "completion", Type: "openai-compatible", Provider: client, ModelAliases: map[string]string{"model": "upstream-model"}, Admission: newAdmissionController(0, 0, 0)}},
		modules:   modules.NewPipeline([]modules.Module{module}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.CompletionRequest{Model: "model", Prompt: "private"}
	response, err := router.Completions(t.Context(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "private"}}}, CompletionRequest: &request,
	})
	if err != nil {
		t.Fatal(err)
	}
	if module.pre != 1 || module.post != 1 || module.response == nil || module.response.Usage.TotalTokens != 6 {
		t.Fatalf("module lifecycle incomplete: module=%+v response=%+v", module, response)
	}
	if client.calls != 1 || client.request.Prompt != "masked" || client.request.Model != "upstream-model" {
		t.Fatalf("effective prompt was not sent: %+v", client.request)
	}
}

func TestValidateCompletionResultUsesPromptAndChoiceCounts(t *testing.T) {
	n := 2
	request := openai.CompletionRequest{Prompt: []string{"one", "two"}, N: &n}
	response := validCompletionResponse()
	response.Choices = []openai.CompletionChoice{
		{Index: 0, Text: "a", FinishReason: "stop"},
		{Index: 1, Text: "b", FinishReason: "stop"},
		{Index: 2, Text: "c", FinishReason: "stop"},
		{Index: 3, Text: "d", FinishReason: "stop"},
	}
	if err := validateCompletionResult(response, request); err != nil {
		t.Fatal(err)
	}
	response.Choices = response.Choices[:3]
	if err := validateCompletionResult(response, request); err == nil {
		t.Fatal("incomplete multi-prompt response was accepted")
	}
}

func TestRouterCompletionsRequiresNativeAdapter(t *testing.T) {
	client := &affinityResponseClient{id: "regular"}
	router := Router{
		endpoints: []Endpoint{{Name: "regular", Type: "demo", Provider: client, Admission: newAdmissionController(0, 0, 0)}},
		modules:   modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.CompletionRequest{Model: "model", Prompt: "input"}
	_, err := router.Completions(t.Context(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "input"}}}, CompletionRequest: &request,
	})
	if !errors.Is(err, ErrCompletionsUnsupported) || client.calls != 0 {
		t.Fatalf("unsupported adapter was called: calls=%d err=%v", client.calls, err)
	}
}

func TestCompletionDecoderRejectsOversizeAndMalformedLogprobs(t *testing.T) {
	reader := &embeddingLimitReader{}
	if _, err := decodeCompletionResponse(reader); err == nil || reader.read != maxResponseJSONBytes+1 {
		t.Fatalf("read=%d err=%v", reader.read, err)
	}
	response := validCompletionResponse()
	response.Choices[0].Logprobs = &openai.CompletionLogprobs{Tokens: []string{"a"}}
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeCompletionResponse(strings.NewReader(string(payload))); err == nil {
		t.Fatal("inconsistent completion logprobs were accepted")
	}
}
