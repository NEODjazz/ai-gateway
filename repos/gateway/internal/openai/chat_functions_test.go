package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLegacyFunctionChoiceJSON(t *testing.T) {
	for _, payload := range []string{`"none"`, `"auto"`, `{"name":"weather"}`} {
		var request ChatCompletionRequest
		if err := json.Unmarshal([]byte(`{"function_call":`+payload+`}`), &request); err != nil {
			t.Fatalf("valid choice %s rejected: %v", payload, err)
		}
		encoded, err := json.Marshal(request.FunctionCall)
		if err != nil || string(encoded) != payload {
			t.Fatalf("choice did not round trip: got=%s err=%v", encoded, err)
		}
	}
	for _, payload := range []string{`"required"`, `{"name":"weather","extra":true}`, `{}`, `42`} {
		var request ChatCompletionRequest
		if json.Unmarshal([]byte(`{"function_call":`+payload+`}`), &request) == nil {
			t.Fatalf("invalid choice %s accepted", payload)
		}
	}
}

func TestValidateLegacyFunctionRequest(t *testing.T) {
	auto := &LegacyFunctionChoice{Mode: "auto"}
	named := &LegacyFunctionChoice{Name: "weather"}
	parallel := true
	function := FunctionDefinition{Name: "weather", Description: "Current weather", Parameters: map[string]any{"type": "object"}}
	valid := ChatCompletionRequest{
		Functions: []FunctionDefinition{function}, FunctionCall: named,
		Messages: []Message{{Role: "assistant", FunctionCall: &FunctionCall{Name: "weather", Arguments: `{}`}}, {Role: "function", Name: "weather", Content: `{}`}},
	}
	if err := ValidateLegacyFunctionRequest(valid); err != nil {
		t.Fatal(err)
	}
	tests := []ChatCompletionRequest{
		{Functions: []FunctionDefinition{{Name: "lookup", PromptCacheBreakpoint: &PromptCacheBreakpoint{Mode: "explicit"}}}},
		{Functions: []FunctionDefinition{function}, Tools: []Tool{{Type: "function", Function: function}}},
		{Functions: []FunctionDefinition{function, function}},
		{Functions: []FunctionDefinition{function}, FunctionCall: &LegacyFunctionChoice{Name: "other"}},
		{Functions: []FunctionDefinition{function}, FunctionCall: &LegacyFunctionChoice{}},
		{Functions: []FunctionDefinition{function}, FunctionCall: &LegacyFunctionChoice{Name: "weather", Mode: "auto"}},
		{FunctionCall: auto},
		{Functions: []FunctionDefinition{function}, ParallelToolCalls: &parallel},
		{Messages: []Message{{Role: "user", FunctionCall: &FunctionCall{Name: "weather", Arguments: `{}`}}}},
		{Messages: []Message{{Role: "function", Content: `{}`}}},
		{Functions: []FunctionDefinition{{Name: "bad name"}}},
	}
	for index, request := range tests {
		if err := ValidateLegacyFunctionRequest(request); err == nil {
			t.Errorf("invalid request %d accepted", index)
		}
	}
}

func TestLegacyFunctionsAreIncludedInTokenEstimate(t *testing.T) {
	base := ChatCompletionRequest{Messages: []Message{{Role: "user", Content: "weather"}}}
	request := base
	request.Functions = []FunctionDefinition{{Name: "weather", Parameters: map[string]any{"description": strings.Repeat("schema", 1000)}}}
	request.FunctionCall = &LegacyFunctionChoice{Name: "weather"}
	if ChatInputTokens(request) <= ChatInputTokens(base)+1000 {
		t.Fatal("legacy function schema or choice omitted from token estimate")
	}
	request.Messages = append(request.Messages, Message{Role: "assistant", FunctionCall: &FunctionCall{Name: "weather", Arguments: strings.Repeat("argument", 1000)}})
	if ChatInputTokens(request) <= ChatInputTokens(base)+2000 {
		t.Fatal("legacy function arguments omitted from token estimate")
	}
}
