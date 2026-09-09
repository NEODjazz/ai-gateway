package openai

import (
	"encoding/json"
	"testing"
)

func TestChatGenerationOptionValidation(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{}`, true},
		{`{"reasoning_effort":"high","n":2,"logprobs":true,"top_logprobs":0,"frequency_penalty":0,"presence_penalty":-2,"logit_bias":{"10":-100}}`, true},
		{`{"n":0}`, false},
		{`{"n":129}`, false},
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
