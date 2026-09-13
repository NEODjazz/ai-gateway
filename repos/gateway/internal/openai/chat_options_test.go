package openai

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestChatGenerationOptionValidation(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{}`, true},
		{`{"modalities":["text"]}`, true},
		{`{"modalities":[]}`, false},
		{`{"modalities":["audio"]}`, false},
		{`{"modalities":["text","text"]}`, false},
		{`{"metadata":{"trace":"one"},"store":false,"prompt_cache_options":{"mode":"explicit","ttl":"30m"},"prompt_cache_retention":"24h","prediction":{"type":"content","content":"expected"},"reasoning_effort":"high","n":2,"safety_identifier":"hashed-user","user":"legacy-user","logprobs":true,"top_logprobs":0,"frequency_penalty":0,"presence_penalty":-2,"min_p":0.05,"top_k":40,"top_a":0.2,"repetition_penalty":1.1,"logit_bias":{"10":-100}}`, true},
		{`{"web_search_options":{"search_context_size":"high","user_location":{"type":"approximate","approximate":{"city":"Paris","country":"FR","region":"Ile-de-France","timezone":"Europe/Paris"}}}}`, true},
		{`{"web_search_options":{}}`, true},
		{`{"web_search_options":{"max_uses":1}}`, true},
		{`{"web_search_options":{"max_uses":0}}`, false},
		{`{"web_search_options":{"max_uses":6}}`, false},
		{`{"web_search_options":{"search_context_size":"huge"}}`, false},
		{`{"web_search_options":{"user_location":{"type":"precise","approximate":{}}}}`, false},
		{`{"web_search_options":{"user_location":{"type":"approximate"}}}`, false},
		{`{"prompt_cache_retention":"in_memory"}`, true},
		{`{"prompt_cache_retention":"1h"}`, false},
		{`{"prompt_mode":"reasoning"}`, true},
		{`{"prompt_mode":"fast"}`, false},
		{`{"prediction":{"type":"content","content":[{"type":"text","text":"one","prompt_cache_breakpoint":{"mode":"explicit"}},{"type":"text","text":"two"}]}}`, true},
		{`{"prediction":{"type":"other","content":"expected"}}`, false},
		{`{"prediction":{"type":"content"}}`, false},
		{`{"prediction":{"type":"content","content":[{"type":"image","text":"x"}]}}`, false},
		{`{"prediction":{"type":"content","content":[{"type":"text","text":"x","extra":true}]}}`, false},
		{`{"prediction":{"type":"content","content":[{"type":"text","text":"x","prompt_cache_breakpoint":{"mode":"implicit"}}]}}`, false},
		{`{"prompt_cache_options":{}}`, true},
		{`{"prompt_cache_options":{"mode":"invalid"}}`, false},
		{`{"prompt_cache_options":{"ttl":"24h"}}`, false},
		{`{"prompt_cache_options":{"comparison_response_id":"resp_reference"}}`, false},
		{`{"metadata":{"trace":"` + strings.Repeat("я", 512) + `"}}`, true},
		{`{"metadata":{"trace":"` + strings.Repeat("я", 513) + `"}}`, false},
		{`{"n":0}`, false},
		{`{"n":129}`, false},
		{`{"safety_identifier":"` + strings.Repeat("я", 64) + `"}`, true},
		{`{"safety_identifier":"` + strings.Repeat("я", 65) + `"}`, false},
		{`{"safety_identifier":"` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `"}`, false},
		{`{"reasoning_effort":"default"}`, true},
		{`{"reasoning_effort":"unexpected"}`, false},
		{`{"logprobs":true,"top_logprobs":21}`, false},
		{`{"logprobs":true,"top_logprobs":-1}`, false},
		{`{"logprobs":false,"top_logprobs":0}`, false},
		{`{"top_logprobs":1}`, false},
		{`{"frequency_penalty":2.1}`, false},
		{`{"presence_penalty":-2.1}`, false},
		{`{"min_p":-0.1}`, false},
		{`{"min_p":1.1}`, false},
		{`{"top_k":-1}`, false},
		{`{"top_k":1000001}`, false},
		{`{"top_a":-0.1}`, false},
		{`{"top_a":1.1}`, false},
		{`{"repetition_penalty":0}`, false},
		{`{"repetition_penalty":-1}`, false},
		{`{"logit_bias":{"10":101}}`, false},
		{`{"logit_bias":{"-1":1}}`, false},
		{`{"logit_bias":{"word":1}}`, false},
	} {
		t.Run(tc.body, func(t *testing.T) {
			var options ChatGenerationOptions
			if err := json.Unmarshal([]byte(tc.body), &options); err != nil {
				t.Fatal(err)
			}
			if message := options.Validate(); (message == "") != tc.valid {
				t.Fatalf("valid=%v message=%s", tc.valid, message)
			}
		})
	}
}

func TestChatGenerationOptionRejectsNonFiniteNumbers(t *testing.T) {
	values := []*float64{pointerFloat(math.NaN()), pointerFloat(math.Inf(1)), pointerFloat(math.Inf(-1))}
	for _, value := range values {
		for _, options := range []ChatGenerationOptions{
			{FrequencyPenalty: value},
			{PresencePenalty: value},
			{MinP: value},
			{TopA: value},
			{RepetitionPenalty: value},
		} {
			if message := options.Validate(); message == "" {
				t.Fatalf("non-finite value accepted: %+v", options)
			}
		}
	}
}

func pointerFloat(value float64) *float64 { return &value }
