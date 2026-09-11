package openai

import (
	"encoding/json"
	"testing"
)

func TestProviderCostTicksAreDecodedButNotExposed(t *testing.T) {
	ticks := int64(37_756_000)
	chat := &Usage{}
	responses := &ResponseUsage{}
	images := &ImageUsage{}
	for name, test := range map[string]struct {
		target any
		cost   func() *int64
	}{
		"chat":      {target: chat, cost: func() *int64 { return chat.ProviderCostUSDTicks }},
		"responses": {target: responses, cost: func() *int64 { return responses.ProviderCostUSDTicks }},
		"images":    {target: images, cost: func() *int64 { return images.ProviderCostUSDTicks }},
	} {
		t.Run(name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(`{"input_tokens":1,"output_tokens":2,"total_tokens":3,"cost_in_usd_ticks":37756000}`), test.target); err != nil {
				t.Fatal(err)
			}
			if got := test.cost(); got == nil || *got != ticks {
				t.Fatalf("ticks=%v", got)
			}
			encoded, err := json.Marshal(test.target)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) == "" || json.Valid(encoded) == false || containsJSONKey(encoded, "cost_in_usd_ticks") {
				t.Fatalf("provider cost leaked into public response: %s", encoded)
			}
		})
	}
	if chat.TotalTokens != 3 || responses.TotalTokens != 3 || images.TotalTokens != 3 {
		t.Fatalf("standard usage fields were lost: chat=%+v responses=%+v images=%+v", chat, responses, images)
	}
}

func containsJSONKey(payload []byte, key string) bool {
	var value map[string]any
	_ = json.Unmarshal(payload, &value)
	_, found := value[key]
	return found
}
