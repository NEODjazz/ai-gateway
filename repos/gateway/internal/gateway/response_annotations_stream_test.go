package gateway

import (
	"encoding/json"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestSyntheticResponseAnnotations(t *testing.T) {
	var response openai.ResponseResponse
	if err := json.Unmarshal([]byte(`{"id":"r","output":[{"id":"m","type":"message","content":[{"type":"output_text","text":"source","annotations":[{"type":"url_citation","url":"https://example.com","title":"source","start_index":0,"end_index":6}]}]}]}`), &response); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	err := synthesizeResponseStream(response, func(kind, payload string) error {
		var data map[string]any
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			return err
		}
		switch kind {
		case "response.content_part.added", "response.content_part.done":
			seen[kind]++
			annotations, ok := data["part"].(map[string]any)["annotations"].([]any)
			want := 0
			if kind == "response.content_part.done" {
				want = 1
			}
			if !ok || len(annotations) != want {
				t.Fatalf("annotations=%v", data)
			}
		case "response.output_text.annotation.added":
			seen[kind]++
			if data["annotation_index"] != float64(0) || data["content_index"] != float64(0) || data["item_id"] != "m" || data["annotation"].(map[string]any)["url"] != "https://example.com" {
				t.Fatalf("annotation event=%v", data)
			}
		}
		return nil
	})
	if err != nil || len(seen) != 3 || seen["response.output_text.annotation.added"] != 1 {
		t.Fatalf("err=%v seen=%v", err, seen)
	}
}
