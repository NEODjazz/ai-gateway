package openai

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestResponseOptionsValidation(t *testing.T) {
	for _, body := range []string{`{"max_output_tokens":1}`, `{"max_tokens":1}`, `{"max_output_tokens":null,"max_tokens":null}`, `{}`, `{"top_logprobs":null,"truncation":null}`, `{"top_logprobs":0,"truncation":"auto"}`, `{"top_logprobs":20,"truncation":"disabled"}`, `{"service_tier":"priority"}`, `{"text":{"verbosity":"low"}}`, `{"text":{"verbosity":null}}`, `{"frequency_penalty":-2,"presence_penalty":2,"max_tool_calls":1000}`, `{"prompt_cache_options":{"mode":"explicit","ttl":"30m","comparison_response_id":"resp_reference"},"prompt_cache_retention":"24h"}`} {
		var request ResponseRequest
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		if message := request.Validate(); message != "" {
			t.Fatalf("%s: %s", body, message)
		}
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
	for _, request := range []ResponseRequest{
		{FrequencyPenalty: &below}, {FrequencyPenalty: &nan}, {PresencePenalty: &above},
		{MaxToolCalls: &tooFew}, {MaxToolCalls: &tooMany},
	} {
		if request.Validate() == "" {
			t.Fatalf("invalid controls accepted: %+v", request)
		}
	}
}

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
