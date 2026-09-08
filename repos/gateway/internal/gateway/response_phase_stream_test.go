package gateway

import (
	"encoding/json"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestSyntheticResponsePreservesMessagePhase(t *testing.T) {
	var response openai.ResponseResponse
	if err := json.Unmarshal([]byte(`{"id":"r","status":"completed","output":[{"id":"reason","type":"message","phase":"commentary"}]}`), &response); err != nil {
		t.Fatal(err)
	}
	checked := 0
	err := synthesizeResponseStream(response, func(kind, payload string) error {
		var data map[string]any
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			return err
		}
		var item map[string]any
		switch kind {
		case "response.output_item.done":
			item = data["item"].(map[string]any)
		case "response.completed":
			item = data["response"].(map[string]any)["output"].([]any)[0].(map[string]any)
		default:
			return nil
		}
		checked++
		if item["phase"] != "commentary" {
			t.Error("message phase was lost")
		}
		return nil
	})
	if err != nil || checked != 2 {
		t.Fatalf("err=%v checked=%d", err, checked)
	}
}
