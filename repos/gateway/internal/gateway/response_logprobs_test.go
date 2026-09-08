package gateway

import (
	"ai-gateway-gateway/internal/openai"
	"encoding/json"
	"testing"
)

func TestSyntheticResponseLogprobs(t *testing.T) {
	var response openai.ResponseResponse
	if err := json.Unmarshal([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"A","logprobs":[{"token":"A","logprob":-0.1,"bytes":[65],"top_logprobs":[{"token":"B","logprob":-2,"bytes":[66]}]}]}]}]}`), &response); err != nil {
		t.Fatal(err)
	}
	checked := 0
	err := synthesizeResponseStream(response, func(kind, payload string) error {
		var data map[string]any
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			return err
		}
		switch kind {
		case "response.output_text.delta", "response.output_text.done":
		case "response.content_part.done":
			data = data["part"].(map[string]any)
		default:
			return nil
		}
		checked++
		values, ok := data["logprobs"].([]any)
		if !ok || len(values) != 1 {
			t.Errorf("%s: logprobs missing", kind)
			return nil
		}
		token := values[0].(map[string]any)
		if token["token"] != "A" || token["logprob"] != -0.1 || len(token["top_logprobs"].([]any)) != 1 {
			t.Errorf("%s: corrupt logprobs", kind)
		}
		return nil
	})
	if err != nil || checked != 3 {
		t.Fatalf("err=%v checked=%d", err, checked)
	}
}
