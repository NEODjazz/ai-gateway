package gateway

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestSyntheticResponseReasoningSummary(t *testing.T) {
	response := openai.ResponseResponse{ID: "r", Status: "completed", Output: []openai.ResponseOutputItem{{ID: "reason", Type: "reasoning", Summary: []openai.ResponseOutputContent{{Type: "summary_text", Text: "first"}, {Type: "summary_text", Text: ""}}}}}
	before, _ := json.Marshal(response)
	var kinds []string
	err := synthesizeResponseStream(response, func(kind, payload string) error {
		var data map[string]any
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			return err
		}
		if data["sequence_number"] != float64(len(kinds)) {
			t.Fatalf("sequence=%v", data)
		}
		kinds = append(kinds, kind)
		if len(kinds) >= 3 && len(kinds) <= 10 {
			n := len(kinds) - 3
			if data["summary_index"] != float64(n/4) || data["output_index"] != float64(0) || data["item_id"] != "reason" {
				t.Fatalf("indices=%v", data)
			}
			text := "first"
			if n/4 == 1 {
				text = ""
			}
			switch n % 4 {
			case 0, 3:
				part, ok := data["part"].(map[string]any)
				if !ok {
					t.Fatalf("part missing: %v", data)
				}
				want := text
				if n%4 == 0 {
					want = ""
				}
				if part["type"] != "summary_text" || part["text"] != want {
					t.Fatalf("part=%v", part)
				}
			case 1:
				if data["delta"] != text {
					t.Fatalf("delta=%v", data)
				}
			case 2:
				if data["text"] != text {
					t.Fatalf("text=%v", data)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"response.created", "response.output_item.added"}
	for range 2 {
		want = append(want, "response.reasoning_summary_part.added", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "response.reasoning_summary_part.done")
	}
	want = append(want, "response.output_item.done", "response.completed")
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("events=%v", kinds)
	}
	after, _ := json.Marshal(response)
	if string(before) != string(after) {
		t.Fatal("input mutated")
	}
	stopped := errors.New("writer stopped")
	for stop := 3; stop <= 10; stop++ {
		calls := 0
		err := synthesizeResponseStream(response, func(string, string) error {
			calls++
			if calls == stop {
				return stopped
			}
			return nil
		})
		if !errors.Is(err, stopped) || calls != stop {
			t.Fatalf("stop=%d calls=%d err=%v", stop, calls, err)
		}
	}
}
