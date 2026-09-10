package openai

import (
	"encoding/base64"
	"encoding/json"
	"errors"
)

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
