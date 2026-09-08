package openai

import (
	"encoding/json"
	"testing"
)

func TestResponseOptionsValidation(t *testing.T) {
	for _, body := range []string{`{}`, `{"top_logprobs":null,"truncation":null}`, `{"top_logprobs":0,"truncation":"auto"}`, `{"top_logprobs":20,"truncation":"disabled"}`} {
		var request ResponseRequest
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		if message := request.Validate(); message != "" {
			t.Fatalf("%s: %s", body, message)
		}
	}
}
