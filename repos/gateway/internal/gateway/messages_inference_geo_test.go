package gateway

import (
	"encoding/json"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestMessagesInferenceGeoValidation(t *testing.T) {
	for _, geo := range []string{"global", "us"} {
		var request messagesRequest
		body := `{"model":"m","max_tokens":10,"inference_geo":"` + geo + `","messages":[{"role":"user","content":"hello"}]}`
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		chat, err := request.chat()
		if err != nil || chat.AnthropicInferenceGeo != geo {
			t.Fatalf("geo=%q chat=%+v err=%v", geo, chat, err)
		}
	}
	var invalid messagesRequest
	if err := json.Unmarshal([]byte(`{"model":"m","max_tokens":10,"inference_geo":"eu","messages":[{"role":"user","content":"hello"}]}`), &invalid); err != nil {
		t.Fatal(err)
	}
	if _, err := invalid.chat(); err == nil {
		t.Fatal("unsupported inference_geo accepted")
	}
}

func TestMessagesUsageIncludesReportedInferenceGeo(t *testing.T) {
	usage := messagesUsage(openai.Usage{InferenceGeo: "us"})
	if usage["inference_geo"] != "us" {
		t.Fatalf("usage=%v", usage)
	}
}
