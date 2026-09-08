package openai

import (
	"encoding/json"
	"testing"
)

func TestResponseOptionsValidation(t *testing.T) {
	for _, body := range []string{`{"max_output_tokens":1}`, `{"max_tokens":1}`, `{"max_output_tokens":null,"max_tokens":null}`, `{}`, `{"top_logprobs":null,"truncation":null}`, `{"top_logprobs":0,"truncation":"auto"}`, `{"top_logprobs":20,"truncation":"disabled"}`} {
		var request ResponseRequest
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		if message := request.Validate(); message != "" {
			t.Fatalf("%s: %s", body, message)
		}
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
