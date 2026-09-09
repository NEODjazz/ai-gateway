package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func legacyFunctionRequest() openai.ChatCompletionRequest {
	return openai.ChatCompletionRequest{
		Model: "test-model", Messages: []openai.Message{{Role: "user", Content: "weather"}},
		Functions:    []openai.FunctionDefinition{{Name: "weather", Parameters: map[string]any{"type": "object"}}},
		FunctionCall: &openai.LegacyFunctionChoice{Name: "weather"},
	}
}

func TestNativeAdaptersRejectLegacyFunctionCalling(t *testing.T) {
	request := legacyFunctionRequest()
	for name, client := range map[string]Client{"anthropic": Anthropic{}, "gemini": Gemini{}, "ollama": Ollama{}, "demo": Demo{}} {
		t.Run(name, func(t *testing.T) {
			var failure *Error
			err := validateChatAdapter(client, request)
			if !errors.As(err, &failure) || failure.UpstreamCode != "unsupported_parameter" || failure.Param != "functions" {
				t.Fatalf("unexpected error: %#v", err)
			}
		})
	}
}

func TestOpenAICompatibleRoundTripsLegacyFunctionCall(t *testing.T) {
	var upstream openAICompatibleChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{ID: "legacy", Model: upstream.Model, Choices: []openai.Choice{{Index: 0, FinishReason: "function_call", Message: openai.Message{Role: "assistant", FunctionCall: &openai.FunctionCall{Name: "weather", Arguments: `{"city":"Moscow"}`}}}}})
	}))
	defer server.Close()
	response, err := NewOpenAICompatible(server.URL, "", false).ChatCompletions(context.Background(), legacyFunctionRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(upstream.Functions) != 1 || upstream.FunctionCall == nil || upstream.FunctionCall.Name != "weather" {
		t.Fatalf("legacy request was not forwarded: %+v", upstream)
	}
	if response.Choices[0].Message.FunctionCall == nil || response.Choices[0].Message.FunctionCall.Arguments != `{"city":"Moscow"}` {
		t.Fatalf("legacy response was not preserved: %+v", response)
	}
}

func TestOpenAICompatibleCollectsStreamingLegacyFunctionCall(t *testing.T) {
	payload := "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"function_call\":{\"name\":\"weather\",\"arguments\":\"{\\\"city\\\":\"}},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"function_call\":{\"arguments\":\"\\\"Moscow\\\"}\"}},\"finish_reason\":\"function_call\"}]}\n\n" +
		"data: [DONE]\n\n"
	response, err := streamChatCompletionData(strings.NewReader(payload), "test-model", nil)
	if err != nil {
		t.Fatal(err)
	}
	call := response.Choices[0].Message.FunctionCall
	if call == nil || call.Name != "weather" || call.Arguments != `{"city":"Moscow"}` {
		t.Fatalf("unexpected accumulated function call: %+v", call)
	}
}

func TestOpenAICompatibleRejectsInvalidLegacyFunctionResponses(t *testing.T) {
	request := legacyFunctionRequest()
	incomplete := openai.ChatCompletionResponse{Choices: []openai.Choice{{Message: openai.Message{FunctionCall: &openai.FunctionCall{Name: "weather"}}}}}
	if err := validateChatCompletionEnvelope(incomplete); err == nil {
		t.Fatal("incomplete function response envelope accepted")
	}
	undeclared := openai.ChatCompletionResponse{Choices: []openai.Choice{{Message: openai.Message{FunctionCall: &openai.FunctionCall{Name: "other", Arguments: `{}`}}}}}
	if err := validateRequestedLegacyFunctionCalls(request, undeclared); err == nil {
		t.Fatal("undeclared function call accepted")
	}
	tooLarge := &openai.FunctionCall{Name: "weather", Arguments: strings.Repeat("x", openai.MaxChatFunctionArgumentsChars+1)}
	if err := mergeLegacyFunctionCallDelta(new(*openai.FunctionCall), tooLarge); err == nil {
		t.Fatal("oversized streamed function call accepted")
	}
}

func TestLegacyFunctionsRequireToolsCapabilityAndBypassSemanticCache(t *testing.T) {
	request := legacyFunctionRequest()
	if got := strings.Join(requiredChatCapabilities(request, true), ","); got != "chat,stream,tools" {
		t.Fatalf("unexpected capabilities: %s", got)
	}
	if _, _, ok := semanticRequest(modules.RequestContext{CredentialID: "credential", Request: request}, Endpoint{Name: "test"}); ok {
		t.Fatal("semantic cache accepted legacy function request")
	}
	base := modules.RequestContext{CredentialID: "credential", Request: request}
	changed := base
	changed.Request.FunctionCall = &openai.LegacyFunctionChoice{Mode: "none"}
	if providerCacheKey("chat", base) == providerCacheKey("chat", changed) {
		t.Fatal("exact cache ignored legacy function choice")
	}
}
