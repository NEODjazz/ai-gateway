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

func TestGeminiSafetySettingsAreBoundedAndUnique(t *testing.T) {
	valid := []GeminiSafetySetting{{Category: "HARM_CATEGORY_HARASSMENT", Threshold: "BLOCK_ONLY_HIGH"}}
	if err := ValidateGeminiSafetySettings(valid); err != nil {
		t.Fatal(err)
	}
	for _, settings := range [][]GeminiSafetySetting{
		{{Category: "UNKNOWN", Threshold: "OFF"}},
		{{Category: "HARM_CATEGORY_HARASSMENT", Threshold: "UNKNOWN"}},
		{{Category: "HARM_CATEGORY_HARASSMENT", Threshold: "OFF"}, {Category: "HARM_CATEGORY_HARASSMENT", Threshold: "BLOCK_ONLY_HIGH"}},
		{{Category: "HARM_CATEGORY_HARASSMENT", Threshold: "OFF"}, {Category: "HARM_CATEGORY_HATE_SPEECH", Threshold: "OFF"}, {Category: "HARM_CATEGORY_SEXUALLY_EXPLICIT", Threshold: "OFF"}, {Category: "HARM_CATEGORY_DANGEROUS_CONTENT", Threshold: "OFF"}, {Category: "HARM_CATEGORY_CIVIC_INTEGRITY", Threshold: "OFF"}, {Category: "HARM_CATEGORY_JAILBREAK", Threshold: "OFF"}, {Category: "HARM_CATEGORY_HARASSMENT", Threshold: "BLOCK_ONLY_HIGH"}},
	} {
		if err := ValidateGeminiSafetySettings(settings); err == nil {
			t.Fatalf("invalid settings accepted: %+v", settings)
		}
	}
}

func TestGeminiCodeExecutionPartsAreBounded(t *testing.T) {
	valid := []GeminiCodeExecutionPart{
		{Index: 0, Code: &GeminiExecutableCode{ID: "exec-1", Language: "PYTHON", Code: "print(2 + 2)"}},
		{Index: 1, Result: &GeminiCodeExecutionResult{ID: "exec-1", Outcome: "OUTCOME_OK", Output: "4\n"}},
	}
	if err := ValidateGeminiCodeExecutionParts(valid); err != nil {
		t.Fatal(err)
	}
	invalid := []GeminiCodeExecutionPart{{Index: 0, Code: &GeminiExecutableCode{Language: "JAVASCRIPT", Code: "1+1"}}, {Index: 0, Result: &GeminiCodeExecutionResult{Outcome: "OUTCOME_OK"}}}
	if err := ValidateGeminiCodeExecutionParts(invalid); err == nil {
		t.Fatal("invalid execution parts accepted")
	}
	oversized := []GeminiCodeExecutionPart{{Index: 0, Result: &GeminiCodeExecutionResult{Outcome: "OUTCOME_FAILED", Output: string(make([]byte, MaxGeminiCodeExecutionBytes+1))}}}
	if err := ValidateGeminiCodeExecutionParts(oversized); err == nil {
		t.Fatal("oversized execution output accepted")
	}
}
