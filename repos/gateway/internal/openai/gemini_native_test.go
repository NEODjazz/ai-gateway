package openai

import (
	"encoding/json"
	"testing"
)

func TestGeminiPartSignatureMarkersAreBoundedAndValidated(t *testing.T) {
	content, err := AddGeminiPartSignature(nil, 2, "c2lnbmVk")
	items, parseErr := GeminiPartSignatures(content)
	if err != nil || parseErr != nil || len(items) != 1 || items[0].Index != 2 || items[0].Signature != "c2lnbmVk" {
		t.Fatalf("items=%+v err=%v parse=%v", items, err, parseErr)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"type":"gemini_part_signature","index":2,"signature":"%%%"}`),
		json.RawMessage(`{"type":"gemini_part_signature","index":128,"signature":"c2lnbmVk"}`),
		json.RawMessage(`{"type":"gemini_part_signature","index":2,"signature":"c2lnbmVk","extra":true}`),
	} {
		if _, err := GeminiPartSignatures([]json.RawMessage{raw}); err == nil {
			t.Fatalf("invalid marker accepted: %s", raw)
		}
	}
}
