package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
)

type CompletionPromptKind uint8

const (
	CompletionPromptText CompletionPromptKind = iota
	CompletionPromptTexts
	CompletionPromptTokens
	CompletionPromptTokenArrays
)

type CompletionPromptInfo struct {
	Kind       CompletionPromptKind
	Count      int
	TokenCount int
	Texts      []string
}

// InspectCompletionPrompt validates the four prompt shapes accepted by the
// legacy completions API. A missing or null prompt is an empty text prompt.
func InspectCompletionPrompt(value any) (CompletionPromptInfo, error) {
	if value == nil {
		return CompletionPromptInfo{Kind: CompletionPromptText, Count: 1, Texts: []string{""}}, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return CompletionPromptInfo{}, errors.New("prompt must be a string, an array of strings, an array of token IDs, or an array of token-ID arrays")
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return CompletionPromptInfo{Kind: CompletionPromptText, Count: 1, Texts: []string{""}}, nil
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return CompletionPromptInfo{}, errors.New("prompt contains an invalid string")
		}
		return CompletionPromptInfo{Kind: CompletionPromptText, Count: 1, Texts: []string{text}}, nil
	}
	if raw[0] != '[' {
		return CompletionPromptInfo{}, errors.New("prompt must be a string, an array of strings, an array of token IDs, or an array of token-ID arrays")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return CompletionPromptInfo{}, errors.New("prompt contains an invalid array")
	}
	if len(items) == 0 {
		return CompletionPromptInfo{Kind: CompletionPromptTokens, Count: 1}, nil
	}
	if len(items) > 128 {
		return CompletionPromptInfo{}, errors.New("prompt cannot contain more than 128 prompts")
	}
	switch bytes.TrimSpace(items[0])[0] {
	case '"':
		texts := make([]string, len(items))
		for index, item := range items {
			if err := json.Unmarshal(item, &texts[index]); err != nil {
				return CompletionPromptInfo{}, errors.New("prompt arrays cannot mix strings and token IDs")
			}
		}
		return CompletionPromptInfo{Kind: CompletionPromptTexts, Count: len(texts), Texts: texts}, nil
	case '[':
		total := 0
		for _, item := range items {
			count, err := inspectTokenArray(item)
			if err != nil {
				return CompletionPromptInfo{}, err
			}
			if total > intMax()-count {
				total = intMax()
			} else {
				total += count
			}
		}
		return CompletionPromptInfo{Kind: CompletionPromptTokenArrays, Count: len(items), TokenCount: total}, nil
	default:
		count, err := inspectTokenArray(raw)
		if err != nil {
			return CompletionPromptInfo{}, err
		}
		return CompletionPromptInfo{Kind: CompletionPromptTokens, Count: 1, TokenCount: count}, nil
	}
}

func inspectTokenArray(raw []byte) (int, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return 0, errors.New("prompt token IDs must be arrays of nonnegative integers")
	}
	for _, item := range items {
		value := string(bytes.TrimSpace(item))
		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil || parsed > uint64(^uint32(0)>>1) {
			return 0, errors.New("prompt token IDs must be nonnegative 32-bit integers")
		}
	}
	return len(items), nil
}

func CompletionPromptPolicyContent(value any) any {
	info, err := InspectCompletionPrompt(value)
	if err != nil || len(info.Texts) == 0 {
		return ""
	}
	if info.Kind == CompletionPromptText {
		return info.Texts[0]
	}
	content := make([]any, len(info.Texts))
	for index, text := range info.Texts {
		content[index] = text
	}
	return content
}

func ApplyCompletionPromptPolicyContent(prompt, content any) (any, error) {
	info, err := InspectCompletionPrompt(prompt)
	if err != nil {
		return nil, err
	}
	switch info.Kind {
	case CompletionPromptText:
		text, ok := content.(string)
		if !ok {
			return nil, errors.New("policy module changed the completion prompt shape")
		}
		return text, nil
	case CompletionPromptTexts:
		values, ok := content.([]any)
		if !ok || len(values) != info.Count {
			return nil, errors.New("policy module changed the completion prompt shape")
		}
		texts := make([]string, len(values))
		for index, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, errors.New("policy module changed the completion prompt shape")
			}
			texts[index] = text
		}
		return texts, nil
	default:
		return prompt, nil
	}
}

func CompletionPromptCount(value any) int {
	info, err := InspectCompletionPrompt(value)
	if err != nil {
		return 1
	}
	return info.Count
}

func CompletionChoiceCount(prompt any, n *int) (int, error) {
	info, err := InspectCompletionPrompt(prompt)
	if err != nil {
		return 0, err
	}
	choicesPerPrompt := 1
	if n != nil {
		choicesPerPrompt = *n
		if choicesPerPrompt < 1 || choicesPerPrompt > 128 {
			return 0, errors.New("n must be between 1 and 128")
		}
	}
	if info.Count > 128/choicesPerPrompt {
		return 0, errors.New("prompt count multiplied by n cannot exceed 128 choices")
	}
	return info.Count * choicesPerPrompt, nil
}

func intMax() int {
	return int(^uint(0) >> 1)
}
