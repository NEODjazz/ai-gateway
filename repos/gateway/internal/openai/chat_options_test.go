package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatGenerationOptionValidation(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{}`, true},
		{`{"metadata":{"trace":"one"},"store":false,"reasoning_effort":"high","n":2,"safety_identifier":"hashed-user","logprobs":true,"top_logprobs":0,"frequency_penalty":0,"presence_penalty":-2,"logit_bias":{"10":-100}}`, true},
		{`{"metadata":{"trace":"` + strings.Repeat("я", 512) + `"}}`, true},
		{`{"metadata":{"trace":"` + strings.Repeat("я", 513) + `"}}`, false},
		{`{"n":0}`, false},
		{`{"n":129}`, false},
		{`{"safety_identifier":"` + strings.Repeat("я", 64) + `"}`, true},
		{`{"safety_identifier":"` + strings.Repeat("я", 65) + `"}`, false},
		{`{"safety_identifier":"` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `"}`, false},
		{`{"reasoning_effort":"unexpected"}`, false},
		{`{"logprobs":true,"top_logprobs":21}`, false},
		{`{"logprobs":true,"top_logprobs":-1}`, false},
		{`{"logprobs":false,"top_logprobs":0}`, false},
		{`{"top_logprobs":1}`, false},
		{`{"frequency_penalty":2.1}`, false},
		{`{"presence_penalty":-2.1}`, false},
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
