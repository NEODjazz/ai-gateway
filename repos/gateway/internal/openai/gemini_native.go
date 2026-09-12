package openai

import (
	"encoding/base64"
	"encoding/json"
	"errors"
)

const MaxGeminiCodeExecutionBytes = 1 << 20

func ValidateGeminiCodeExecutionParts(parts []GeminiCodeExecutionPart) error {
	if len(parts) > 128 {
		return errors.New("Gemini code execution parts exceed limit")
	}
	total := 0
	seen := make(map[int]bool, len(parts))
	for _, part := range parts {
		if part.Index < 0 || part.Index >= 128 || seen[part.Index] || (part.Code == nil) == (part.Result == nil) {
			return errors.New("invalid Gemini code execution part")
		}
		seen[part.Index] = true
		if part.Code != nil {
			if part.Code.Language != "PYTHON" || part.Code.Code == "" || len(part.Code.ID) > 128 {
				return errors.New("invalid Gemini executable code")
			}
			total += len(part.Code.ID) + len(part.Code.Code)
		} else {
			switch part.Result.Outcome {
			case "OUTCOME_OK", "OUTCOME_FAILED", "OUTCOME_DEADLINE_EXCEEDED":
			default:
				return errors.New("invalid Gemini code execution outcome")
			}
			if len(part.Result.ID) > 128 {
				return errors.New("invalid Gemini code execution result")
			}
			total += len(part.Result.ID) + len(part.Result.Output)
		}
		if total > MaxGeminiCodeExecutionBytes {
			return errors.New("Gemini code execution parts exceed size limit")
		}
	}
	return nil
}

func ValidateGeminiSafetySettings(settings []GeminiSafetySetting) error {
	if len(settings) > 6 {
		return errors.New("Gemini safety settings exceed limit")
	}
	seen := make(map[string]bool, len(settings))
	for _, setting := range settings {
		switch setting.Category {
		case "HARM_CATEGORY_HATE_SPEECH", "HARM_CATEGORY_SEXUALLY_EXPLICIT", "HARM_CATEGORY_DANGEROUS_CONTENT", "HARM_CATEGORY_HARASSMENT", "HARM_CATEGORY_CIVIC_INTEGRITY", "HARM_CATEGORY_JAILBREAK":
		default:
			return errors.New("invalid Gemini safety category")
		}
		switch setting.Threshold {
		case "HARM_BLOCK_THRESHOLD_UNSPECIFIED", "BLOCK_LOW_AND_ABOVE", "BLOCK_MEDIUM_AND_ABOVE", "BLOCK_ONLY_HIGH", "BLOCK_NONE", "OFF":
		default:
			return errors.New("invalid Gemini safety threshold")
		}
		if seen[setting.Category] {
			return errors.New("duplicate Gemini safety category")
		}
		seen[setting.Category] = true
	}
	return nil
}

const geminiPartSignatureMarker = "gemini_part_signature"

type GeminiPartSignature struct {
	Index     int
	Signature string
}

func AddGeminiPartSignature(content []json.RawMessage, index int, signature string) ([]json.RawMessage, error) {
	if index < 0 || index >= 128 || signature == "" {
		return content, errors.New("invalid Gemini part signature")
	}
	if _, err := base64.StdEncoding.DecodeString(signature); err != nil {
		return content, errors.New("Gemini part signature must be valid base64")
	}
	marker, err := json.Marshal(struct {
		Type      string `json:"type"`
		Index     int    `json:"index"`
		Signature string `json:"signature"`
	}{Type: geminiPartSignatureMarker, Index: index, Signature: signature})
	if err != nil {
		return content, err
	}
	if len(marker) > 1<<20 {
		return content, errors.New("Gemini part signature exceeds limit")
	}
	return append(content, marker), nil
}

func GeminiPartSignatures(content []json.RawMessage) ([]GeminiPartSignature, error) {
	result := make([]GeminiPartSignature, 0)
	seen := make(map[int]bool)
	total := 0
	for _, raw := range content {
		var marker map[string]json.RawMessage
		if json.Unmarshal(raw, &marker) != nil {
			continue
		}
		var markerType string
		if json.Unmarshal(marker["type"], &markerType) != nil || markerType != geminiPartSignatureMarker {
			continue
		}
		if len(marker) != 3 {
			return nil, errors.New("invalid Gemini part signature marker")
		}
		var item GeminiPartSignature
		if json.Unmarshal(marker["index"], &item.Index) != nil || json.Unmarshal(marker["signature"], &item.Signature) != nil || item.Index < 0 || item.Index >= 128 || item.Signature == "" || seen[item.Index] {
			return nil, errors.New("invalid Gemini part signature marker")
		}
		if _, err := base64.StdEncoding.DecodeString(item.Signature); err != nil {
			return nil, errors.New("invalid Gemini part signature encoding")
		}
		total += len(item.Signature)
		if total > 1<<20 {
			return nil, errors.New("Gemini part signatures exceed limit")
		}
		seen[item.Index] = true
		result = append(result, item)
	}
	return result, nil
}
