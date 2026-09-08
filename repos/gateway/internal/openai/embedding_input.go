package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
)

const (
	MaxEmbeddingInputs         = 2048
	MaxEmbeddingTokensPerInput = 8192
	MaxEmbeddingTokensTotal    = 300000
)

type EmbeddingInputKind uint8

const (
	EmbeddingInputSingleText EmbeddingInputKind = iota
	EmbeddingInputTextList
	EmbeddingInputTokenIDs
	EmbeddingInputTokenIDLists
)

type EmbeddingInputInfo struct {
	Kind       EmbeddingInputKind
	Count      int
	TokenCount int
	Texts      []string
}

func (i EmbeddingInputInfo) Tokenized() bool {
	return i.Kind == EmbeddingInputTokenIDs || i.Kind == EmbeddingInputTokenIDLists
}

func InspectEmbeddingInput(value any) (EmbeddingInputInfo, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return EmbeddingInputInfo{}, invalidEmbeddingInput()
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return EmbeddingInputInfo{}, invalidEmbeddingInput()
	}
	if raw[0] == '"' {
		var value string
		if json.Unmarshal(raw, &value) != nil || value == "" {
			return EmbeddingInputInfo{}, invalidEmbeddingInput()
		}
		return EmbeddingInputInfo{Kind: EmbeddingInputSingleText, Count: 1, Texts: []string{value}}, nil
	}
	if raw[0] != '[' {
		return EmbeddingInputInfo{}, invalidEmbeddingInput()
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 || len(items) > MaxEmbeddingInputs {
		return EmbeddingInputInfo{}, invalidEmbeddingInput()
	}
	first := bytes.TrimSpace(items[0])
	if len(first) == 0 {
		return EmbeddingInputInfo{}, invalidEmbeddingInput()
	}
	switch first[0] {
	case '"':
		texts := make([]string, len(items))
		for index, item := range items {
			if json.Unmarshal(item, &texts[index]) != nil || texts[index] == "" {
				return EmbeddingInputInfo{}, invalidEmbeddingInput()
			}
		}
		return EmbeddingInputInfo{Kind: EmbeddingInputTextList, Count: len(texts), Texts: texts}, nil
	case '[':
		total := 0
		for _, item := range items {
			count, err := inspectEmbeddingTokenArray(item)
			if err != nil || total > MaxEmbeddingTokensTotal-count {
				return EmbeddingInputInfo{}, invalidEmbeddingInput()
			}
			total += count
		}
		return EmbeddingInputInfo{Kind: EmbeddingInputTokenIDLists, Count: len(items), TokenCount: total}, nil
	default:
		count, err := inspectEmbeddingTokenArray(raw)
		if err != nil {
			return EmbeddingInputInfo{}, invalidEmbeddingInput()
		}
		return EmbeddingInputInfo{Kind: EmbeddingInputTokenIDs, Count: 1, TokenCount: count}, nil
	}
}

func inspectEmbeddingTokenArray(raw []byte) (int, error) {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 || len(items) > MaxEmbeddingTokensPerInput {
		return 0, invalidEmbeddingInput()
	}
	for _, item := range items {
		value := string(bytes.TrimSpace(item))
		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil || parsed > uint64(^uint32(0)>>1) {
			return 0, invalidEmbeddingInput()
		}
	}
	return len(items), nil
}

func invalidEmbeddingInput() error {
	return errors.New("input must be a non-empty string, an array of non-empty strings, an array of token IDs, or an array of token-ID arrays within embedding limits")
}

func EmbeddingInputTokenCount(value any) int {
	info, err := InspectEmbeddingInput(value)
	if err != nil {
		return 1
	}
	if info.Tokenized() {
		return info.TokenCount
	}
	total := 0
	for _, text := range info.Texts {
		count := EstimateContextTokens(text)
		if total > intMax()-count {
			return intMax()
		}
		total += count
	}
	return max(1, total)
}
