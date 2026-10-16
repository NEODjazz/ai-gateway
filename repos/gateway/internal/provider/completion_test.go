package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

type scriptedCompletionStreamClient struct {
	completionTestClient
	streamCalls int
	payloads    []string
	err         error
}

func (c *scriptedCompletionStreamClient) StreamCompletions(_ context.Context, _ openai.CompletionRequest, write CompletionStreamWriter) (openai.CompletionResponse, error) {
	c.streamCalls++
	for _, payload := range c.payloads {
		if err := write(payload); err != nil {
			return openai.CompletionResponse{}, err
		}
	}
	return c.result, c.err
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

func TestRouterCompletionStreamStopsFallbackAfterClientWrite(t *testing.T) {
	first := &scriptedCompletionStreamClient{payloads: []string{`{"id":"cmpl","choices":[{"index":0,"text":"partial"}]}`}, err: errors.New("stream failed")}
	second := &scriptedCompletionStreamClient{completionTestClient: completionTestClient{result: validCompletionResponse()}}
	router := Router{
		endpoints: []Endpoint{
			{Name: "first", Type: "openai-compatible", Provider: first, Admission: newAdmissionController(0, 0, 0)},
			{Name: "second", Type: "openai-compatible", Provider: second, Admission: newAdmissionController(0, 0, 0)},
		},
		modules: modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.CompletionRequest{Model: "model", Prompt: "input", Stream: true}
	_, streamed, err := router.StreamCompletions(t.Context(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "input"}}}, CompletionRequest: &request,
	}, func(string) error { return nil })
	if err == nil || !streamed || first.streamCalls != 1 || second.streamCalls != 0 {
		t.Fatalf("fallback crossed stream boundary: streamed=%v err=%v first=%d second=%d", streamed, err, first.streamCalls, second.streamCalls)
	}
}

func TestRouterCompletionOutputDLPUsesBufferedFallback(t *testing.T) {
	client := &scriptedCompletionStreamClient{completionTestClient: completionTestClient{result: validCompletionResponse()}, payloads: []string{`{"choices":[{"index":0,"text":"must not leak"}]}`}}
	router := Router{
		endpoints: []Endpoint{{Name: "guarded", Type: "openai-compatible", Models: []string{"model"}, Capabilities: []string{"chat", "stream"}, OutputDLPEnabled: true, Provider: client}},
		modules:   modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.CompletionRequest{Model: "model", Prompt: "input", Stream: true}
	writes := 0
	_, streamed, err := router.StreamCompletions(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "input"}}}, CompletionRequest: &request}, func(string) error { writes++; return nil })
	if err != nil || streamed || writes != 0 || client.streamCalls != 0 {
		t.Fatalf("completion output leaked before scanning: streamed=%v writes=%d calls=%d err=%v", streamed, writes, client.streamCalls, err)
	}
}

func TestRouterCompletionStreamRunsPostResponseBilling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"cmpl_stream\",\"object\":\"text_completion\",\"created\":7,\"model\":\"instruct\",\"choices\":[{\"index\":0,\"text\":\"done\",\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	module := &completionLifecycleModule{}
	router := Router{
		endpoints: []Endpoint{{Name: "completion", Type: "openai-compatible", Provider: NewOpenAICompatible(server.URL, "", true), Admission: newAdmissionController(0, 0, 0)}},
		modules:   modules.NewPipeline([]modules.Module{module}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.CompletionRequest{Model: "instruct", Prompt: "private", Stream: true}
	response, streamed, err := router.StreamCompletions(t.Context(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{Model: "instruct", Messages: []openai.Message{{Role: "user", Content: "private"}}}, CompletionRequest: &request,
	}, func(string) error { return nil })
	if err != nil || !streamed || module.post != 1 || module.response == nil || module.response.Usage.TotalTokens != 6 || response.Choices[0].Text != "done" {
		t.Fatalf("stream lifecycle incomplete: streamed=%v err=%v module=%+v response=%+v", streamed, err, module, response)
	}
}

func TestCompletionStreamDeanonymizerJoinsSplitPlaceholder(t *testing.T) {
	var payloads []map[string]any
	write := deanonymizingCompletionStreamWriter(map[string]string{"{{EMAIL_1}}": "user@example.com"}, func(payload string) error {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			return err
		}
		payloads = append(payloads, decoded)
		return nil
	})
	if err := write(`{"choices":[{"index":0,"text":"{{EMA","finish_reason":null}]}`); err != nil {
		t.Fatal(err)
	}
	if err := write(`{"choices":[{"index":0,"text":"IL_1}}","finish_reason":"stop"}]}`); err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, payload := range payloads {
		choice := payload["choices"].([]any)[0].(map[string]any)
		text += choice["text"].(string)
	}
	if text != "user@example.com" {
		t.Fatalf("split completion placeholder was not restored: %q", text)
	}
}

func TestChatStreamDeanonymizerJoinsSplitRefusalPlaceholder(t *testing.T) {
	var refusal strings.Builder
	write := deanonymizingChatStreamWriter(map[string]string{"{{EMAIL_1}}": "user@example.com"}, func(payload string) error {
		var chunk struct {
			Choices []struct {
				Delta struct {
					Refusal string `json:"refusal"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return err
		}
		for _, choice := range chunk.Choices {
			refusal.WriteString(choice.Delta.Refusal)
		}
		return nil
	})
	for _, payload := range []string{
		`{"choices":[{"index":0,"delta":{"refusal":"{{EMA"}}]}`,
		`{"choices":[{"index":0,"delta":{"refusal":"IL_1}}"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`,
	} {
		if err := write(payload); err != nil {
			t.Fatal(err)
		}
	}
	if refusal.String() != "user@example.com" {
		t.Fatalf("split refusal placeholder was not restored: %q", refusal.String())
	}
}

func TestChatStreamDeanonymizerJoinsSplitAudioTranscriptPlaceholder(t *testing.T) {
	var transcript strings.Builder
	write := deanonymizingChatStreamWriter(map[string]string{"{{EMAIL_1}}": "user@example.com"}, func(payload string) error {
		var chunk struct {
			Choices []struct {
				Delta struct {
					Audio struct {
						Transcript string `json:"transcript"`
					} `json:"audio"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return err
		}
		for _, choice := range chunk.Choices {
			transcript.WriteString(choice.Delta.Audio.Transcript)
		}
		return nil
	})
	for _, payload := range []string{
		`{"choices":[{"index":0,"delta":{"audio":{"transcript":"{{EMA"}}}]}`,
		`{"choices":[{"index":0,"delta":{"audio":{"transcript":"IL_1}}"}}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	} {
		if err := write(payload); err != nil {
			t.Fatal(err)
		}
	}
	if transcript.String() != "user@example.com" {
		t.Fatalf("split audio transcript placeholder was not restored: %q", transcript.String())
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

func TestRouterRejectsUnsupportedOllamaPromptBeforeModules(t *testing.T) {
	module := &completionLifecycleModule{}
	router := Router{
		endpoints: []Endpoint{{Name: "ollama", Type: "ollama", Provider: NewOllama("http://unused.invalid", true), Admission: newAdmissionController(0, 0, 0)}},
		modules:   modules.NewPipeline([]modules.Module{module}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.CompletionRequest{Model: "model", Prompt: []string{"one", "two"}}
	_, err := router.Completions(t.Context(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: []any{"one", "two"}}}}, CompletionRequest: &request,
	})
	var failure *Error
	if !errors.As(err, &failure) || failure.Param != "prompt" || module.pre != 0 {
		t.Fatalf("unsupported prompt reached modules: err=%v pre=%d", err, module.pre)
	}
}

func TestRouterRejectsIgnoredOllamaCompletionParameterBeforeModules(t *testing.T) {
	module := &completionLifecycleModule{}
	router := Router{
		endpoints: []Endpoint{{Name: "ollama", Type: "ollama", Provider: NewOllama("http://unused.invalid", true), Admission: newAdmissionController(0, 0, 0)}},
		modules:   modules.NewPipeline([]modules.Module{module}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	bestOf := 2
	request := openai.CompletionRequest{Model: "model", Prompt: "input", BestOf: &bestOf}
	context := modules.RequestContext{
		Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "input"}}}, CompletionRequest: &request,
	}
	_, err := router.Completions(t.Context(), context)
	assertUnsupportedParameter(t, err, "best_of")
	request.Stream = true
	_, _, err = router.StreamCompletions(t.Context(), context, func(string) error { return nil })
	assertUnsupportedParameter(t, err, "best_of")
	if module.pre != 0 {
		t.Fatalf("unsupported completion reached modules: pre=%d", module.pre)
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
