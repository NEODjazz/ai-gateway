package openai

import (
	"encoding/base64"
	"strings"
	"testing"
)

func responseComputerPNGURL(extra int) string {
	payload := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, extra)...)
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(payload)
}

func TestResponseComputerToolAndOutputValidation(t *testing.T) {
	imageURL := responseComputerPNGURL(4)
	request := ResponseRequest{
		Tools:      []ResponseTool{{Type: "computer"}},
		ToolChoice: map[string]any{"type": "computer"},
		Input: []any{map[string]any{
			"type": "computer_call_output", "call_id": "call_1", "status": "completed",
			"output":                     map[string]any{"type": "computer_screenshot", "image_url": imageURL, "detail": "original"},
			"acknowledged_safety_checks": []any{map[string]any{"id": "check_1", "code": "domain", "message": "reviewed"}},
		}},
	}
	if message := request.Validate(); message != "" {
		t.Fatal(message)
	}
	outputs, message := InspectResponseComputerCallOutputs(request.Input)
	if message != "" || len(outputs) != 1 || outputs[0].CallID != "call_1" || outputs[0].ImageURL != imageURL {
		t.Fatalf("outputs=%+v message=%q", outputs, message)
	}
	attachments, err := ResponseImageAttachments(request.Input)
	if err != nil || len(attachments) != 1 || attachments[0].MediaType != "image/png" {
		t.Fatalf("attachments=%+v err=%v", attachments, err)
	}
	projection := TextOnlyProjection(request.Input)
	if strings.Contains(string(mustJSON(t, projection)), imageURL) {
		t.Fatal("computer screenshot leaked into text-only policy projection")
	}
}

func TestResponseComputerFileOutputValidation(t *testing.T) {
	input := []any{map[string]any{
		"type": "computer_call_output", "call_id": "call_1",
		"output": map[string]any{"type": "computer_screenshot", "file_id": "file_owned"},
	}}
	outputs, message := InspectResponseComputerCallOutputs(input)
	if message != "" || len(outputs) != 1 || outputs[0].FileID != "file_owned" {
		t.Fatalf("outputs=%+v message=%q", outputs, message)
	}
	attachments, err := ResponseImageAttachments(input)
	if err != nil || len(attachments) != 0 {
		t.Fatalf("unresolved file must not be treated as inline image: attachments=%+v err=%v", attachments, err)
	}
}

func TestResponseComputerRejectsInvalidContracts(t *testing.T) {
	imageURL := responseComputerPNGURL(0)
	for _, input := range []any{
		[]any{map[string]any{"type": "computer_call_output", "output": map[string]any{"type": "computer_screenshot", "image_url": imageURL}}},
		[]any{map[string]any{"type": "computer_call_output", "call_id": "call", "extra": true, "output": map[string]any{"type": "computer_screenshot", "image_url": imageURL}}},
		[]any{map[string]any{"type": "computer_call_output", "call_id": "call", "output": map[string]any{"type": "computer_screenshot"}}},
		[]any{map[string]any{"type": "computer_call_output", "call_id": "call", "output": map[string]any{"type": "computer_screenshot", "file_id": "file_1", "image_url": imageURL}}},
		[]any{map[string]any{"type": "computer_call_output", "call_id": "call", "output": map[string]any{"type": "computer_screenshot", "image_url": imageURL, "detail": "raw"}}},
		[]any{map[string]any{"type": "computer_call_output", "call_id": "call", "output": map[string]any{"type": "computer_screenshot", "image_url": imageURL}, "acknowledged_safety_checks": []any{map[string]any{"id": "same"}, map[string]any{"id": "same"}}}},
	} {
		if _, message := InspectResponseComputerCallOutputs(input); message == "" {
			t.Fatalf("invalid computer output accepted: %#v", input)
		}
	}
	for _, tools := range [][]ResponseTool{
		{{Type: "computer"}, {Type: "computer"}},
		{{Type: "computer", Name: "unexpected"}},
	} {
		if message := validateResponseTools(tools); message == "" {
			t.Fatalf("invalid computer tools accepted: %+v", tools)
		}
	}
}

func TestResponseComputerScreenshotTokenEstimateIsBounded(t *testing.T) {
	small := []any{map[string]any{"type": "computer_call_output", "call_id": "call", "output": map[string]any{"type": "computer_screenshot", "image_url": responseComputerPNGURL(1)}}}
	large := []any{map[string]any{"type": "computer_call_output", "call_id": "call", "output": map[string]any{"type": "computer_screenshot", "image_url": responseComputerPNGURL(1 << 20)}}}
	if EstimateContextTokens(small) != EstimateContextTokens(large) || EstimateContextTokens(small) < 4096 {
		t.Fatalf("computer screenshot reserve is not bounded: small=%d large=%d", EstimateContextTokens(small), EstimateContextTokens(large))
	}
}
