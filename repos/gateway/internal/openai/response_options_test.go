package openai

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestResponseOptionsValidation(t *testing.T) {
	for _, body := range []string{`{"max_output_tokens":1}`, `{"max_tokens":1}`, `{"max_output_tokens":null,"max_tokens":null}`, `{}`, `{"top_logprobs":null,"truncation":null}`, `{"top_logprobs":0,"truncation":"auto"}`, `{"top_logprobs":20,"truncation":"disabled"}`, `{"service_tier":"priority"}`, `{"text":{"verbosity":"low"}}`, `{"text":{"verbosity":null}}`, `{"frequency_penalty":-2,"presence_penalty":2,"max_tool_calls":1000}`, `{"prompt_cache_options":{"mode":"explicit","ttl":"30m","comparison_response_id":"resp_reference"},"prompt_cache_retention":"24h"}`, `{"stream":true,"stream_options":{"include_obfuscation":false}}`} {
		var request ResponseRequest
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		if message := request.Validate(); message != "" {
			t.Fatalf("%s: %s", body, message)
		}
	}
}

func TestResponseStreamOptionsRequireStreaming(t *testing.T) {
	value := false
	request := ResponseRequest{StreamOptions: &ResponseStreamOptions{IncludeObfuscation: &value}}
	if message := request.Validate(); message != "stream_options requires stream=true" {
		t.Fatalf("unexpected validation result: %q", message)
	}
}

func TestResponseBackgroundRequiresDurableNonStreamingStorage(t *testing.T) {
	store := true
	if message := (ResponseRequest{Background: true, Store: &store}).Validate(); message != "" {
		t.Fatal(message)
	}
	for _, request := range []ResponseRequest{{Background: true}, {Background: true, Store: &store, Stream: true}} {
		if request.Validate() == "" {
			t.Fatalf("invalid background request accepted: %+v", request)
		}
	}
}

func TestResponseRejectsInvalidGenerationControls(t *testing.T) {
	tooFew, tooMany := 0, 1001
	below, above, nan := -2.1, 2.1, math.NaN()
	negative, aboveTemperature, aboveTopP := -0.1, 2.1, 1.1
	for _, request := range []ResponseRequest{
		{FrequencyPenalty: &below}, {FrequencyPenalty: &nan}, {PresencePenalty: &above},
		{Temperature: &negative}, {Temperature: &aboveTemperature}, {Temperature: &nan},
		{TopP: &negative}, {TopP: &aboveTopP}, {TopP: &nan},
		{MaxToolCalls: &tooFew}, {MaxToolCalls: &tooMany},
	} {
		if request.Validate() == "" {
			t.Fatalf("invalid controls accepted: %+v", request)
		}
	}
}

func TestResponseAcceptsGenerationControlBoundaries(t *testing.T) {
	for _, request := range []ResponseRequest{
		{Temperature: float64Pointer(0)}, {Temperature: float64Pointer(2)},
		{TopP: float64Pointer(0)}, {TopP: float64Pointer(1)},
	} {
		if message := request.Validate(); message != "" {
			t.Fatalf("boundary rejected: %+v: %s", request, message)
		}
	}
}

func float64Pointer(value float64) *float64 { return &value }

func TestResponseRejectsInvalidTextVerbosity(t *testing.T) {
	for _, value := range []any{"unknown", 1, true} {
		request := ResponseRequest{Text: map[string]any{"verbosity": value}}
		if message := request.Validate(); message != "text.verbosity must be low, medium, or high" {
			t.Fatalf("value=%v: %q", value, message)
		}
	}
}

func TestResponseRejectsUnknownServiceTier(t *testing.T) {
	if message := (ResponseRequest{ServiceTier: "unknown"}).Validate(); message != "unsupported service_tier value" {
		t.Fatalf("unexpected validation result: %q", message)
	}
}

func TestResponseIncludeValidation(t *testing.T) {
	valid := []string{
		"web_search_call.action.sources",
		"code_interpreter_call.outputs",
		"computer_call_output.output.image_url",
		"file_search_call.results",
		"message.input_image.image_url",
		"message.output_text.logprobs",
		"reasoning.encrypted_content",
	}
	if message := (ResponseRequest{Include: valid}).Validate(); message != "" {
		t.Fatalf("valid include set rejected: %s", message)
	}
	for _, include := range [][]string{
		{"unknown"},
		{"reasoning.encrypted_content", "reasoning.encrypted_content"},
		append(append([]string(nil), valid...), "reasoning.encrypted_content"),
	} {
		if message := (ResponseRequest{Include: include}).Validate(); message == "" {
			t.Fatalf("invalid include set accepted: %v", include)
		}
	}
}

func TestResponseToolChoiceValidation(t *testing.T) {
	tools := []ResponseTool{
		{Type: "function", Name: "lookup"},
		{Type: "mcp", ServerLabel: "documents", AllowedTools: []string{"search"}},
	}
	for _, choice := range []any{
		"none", "auto", "required",
		map[string]any{"type": "function", "name": "lookup"},
		map[string]any{"type": "mcp", "server_label": "documents", "name": "search"},
	} {
		if message := (ResponseRequest{Tools: tools, ToolChoice: choice}).Validate(); message != "" {
			t.Fatalf("choice %#v rejected: %s", choice, message)
		}
	}
	for _, choice := range []any{
		"unknown", "required",
		map[string]any{"type": "function", "name": "missing"},
		map[string]any{"type": "function", "name": "lookup", "extra": true},
		map[string]any{"type": "mcp", "server_label": "documents", "name": "write"},
		map[string]any{"type": "mcp", "server_label": "missing", "name": "search"},
		map[string]any{"type": "unknown", "name": "lookup"},
		42,
	} {
		request := ResponseRequest{Tools: tools, ToolChoice: choice}
		if choice == "required" {
			request.Tools = nil
		}
		if message := request.Validate(); message == "" {
			t.Fatalf("invalid choice accepted: %#v", choice)
		}
	}
}

func TestServiceTierValues(t *testing.T) {
	for _, value := range []string{"", "auto", "default", "on_demand", "flex", "performance", "scale", "priority", "fast", "ultrafast"} {
		if !validServiceTier(value) {
			t.Fatalf("documented service tier rejected: %q", value)
		}
	}
	if validServiceTier("unknown") {
		t.Fatal("unknown service tier accepted")
	}
}

func TestVerbosityValues(t *testing.T) {
	for _, value := range []string{"", "low", "medium", "high"} {
		if !validVerbosity(value) {
			t.Fatalf("documented verbosity rejected: %q", value)
		}
	}
	if validVerbosity("unknown") {
		t.Fatal("unknown verbosity accepted")
	}
}

func TestResponseSafetyIdentifierUsesUnicodeCharacterLimit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		identifier string
		valid      bool
	}{
		{name: "64 Unicode characters", identifier: strings.Repeat("я", 64), valid: true},
		{name: "65 Unicode characters", identifier: strings.Repeat("я", 65), valid: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := (ResponseRequest{SafetyIdentifier: tc.identifier}).Validate()
			if (message == "") != tc.valid {
				t.Fatalf("validation result %q, valid=%v", message, tc.valid)
			}
		})
	}
}

func TestResponseRejectsNonPositiveOutputLimits(t *testing.T) {
	for _, value := range []int{-1, 0} {
		for _, request := range []ResponseRequest{{MaxOutputTokens: &value}, {MaxTokens: &value}} {
			if request.Validate() == "" {
				t.Fatalf("non-positive output limit accepted: %+v", request)
			}
		}
	}
}

func TestResponseRejectsConflictingOutputLimits(t *testing.T) {
	for _, pair := range [][2]int{{1, 1000}, {1000, 1}, {10, 10}} {
		request := ResponseRequest{MaxOutputTokens: &pair[0], MaxTokens: &pair[1]}
		if request.Validate() == "" {
			t.Fatalf("both output caps accepted: %v", pair)
		}
	}
}
