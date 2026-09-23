package gateway

import (
	"encoding/json"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

const maxRAGCitationStreamBytes = 4 << 20

type ragStreamCitations struct {
	results  []vectorSearchResult
	texts    map[int]*strings.Builder
	closed   map[int]bool
	native   map[int]bool
	overflow map[int]bool
	bytes    int
}

func newRAGStreamCitations(results []vectorSearchResult) *ragStreamCitations {
	return &ragStreamCitations{
		results: results, texts: make(map[int]*strings.Builder), closed: make(map[int]bool),
		native: make(map[int]bool), overflow: make(map[int]bool),
	}
}

func (c *ragStreamCitations) decorate(payload string) ([]string, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal([]byte(payload), &envelope) != nil {
		return []string{payload}, nil
	}
	var choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content     json.RawMessage `json:"content"`
			Annotations json.RawMessage `json:"annotations"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	}
	if json.Unmarshal(envelope["choices"], &choices) != nil || len(choices) == 0 {
		return []string{payload}, nil
	}
	type citationDelta struct {
		index   int
		payload string
	}
	var deltas []citationDelta
	for _, choice := range choices {
		if choice.Index < 0 || choice.Index >= 128 || c.closed[choice.Index] {
			continue
		}
		var text string
		if json.Unmarshal(choice.Delta.Content, &text) == nil && text != "" && !c.overflow[choice.Index] {
			if len(text) > maxRAGCitationStreamBytes-c.bytes {
				c.overflow[choice.Index] = true
			} else {
				builder := c.texts[choice.Index]
				if builder == nil {
					builder = &strings.Builder{}
					c.texts[choice.Index] = builder
				}
				_, _ = builder.WriteString(text)
				c.bytes += len(text)
			}
		}
		var native []json.RawMessage
		if json.Unmarshal(choice.Delta.Annotations, &native) == nil && len(native) > 0 {
			c.native[choice.Index] = true
		}
		if choice.FinishReason == nil {
			continue
		}
		c.closed[choice.Index] = true
		if c.native[choice.Index] || c.overflow[choice.Index] || c.texts[choice.Index] == nil {
			continue
		}
		response := openai.ChatCompletionResponse{Choices: []openai.Choice{{Message: openai.Message{Content: c.texts[choice.Index].String()}}}}
		annotations := annotateRAGResponse(response, c.results).Choices[0].Message.Annotations
		if len(annotations) == 0 {
			continue
		}
		chunk, err := ragCitationStreamChunk(envelope, choice.Index, annotations)
		if err != nil {
			return nil, err
		}
		deltas = append(deltas, citationDelta{index: choice.Index, payload: chunk})
	}
	if len(deltas) == 0 {
		return []string{payload}, nil
	}
	var rawChoices []map[string]json.RawMessage
	if json.Unmarshal(envelope["choices"], &rawChoices) != nil {
		return []string{payload}, nil
	}
	postponed := make(map[int]bool, len(deltas))
	for _, delta := range deltas {
		postponed[delta.index] = true
	}
	finishChoices := make([]map[string]json.RawMessage, 0, len(deltas))
	for index, choice := range rawChoices {
		if !postponed[choices[index].Index] || choices[index].FinishReason == nil {
			continue
		}
		finish := make(map[string]json.RawMessage, len(choice))
		for key, value := range choice {
			finish[key] = value
		}
		delete(finish, "logprobs")
		finish["delta"] = json.RawMessage(`{}`)
		finishChoices = append(finishChoices, finish)
		choice["finish_reason"] = json.RawMessage(`null`)
		delete(choice, "stop_sequence")
	}
	contentChunk, err := ragStreamPayloadWithChoices(envelope, rawChoices, false)
	if err != nil {
		return nil, err
	}
	output := make([]string, 0, len(deltas)+2)
	output = append(output, contentChunk)
	for _, delta := range deltas {
		output = append(output, delta.payload)
	}
	finishChunk, err := ragStreamPayloadWithChoices(envelope, finishChoices, true)
	if err != nil {
		return nil, err
	}
	return append(output, finishChunk), nil
}

func ragStreamPayloadWithChoices(envelope map[string]json.RawMessage, choices []map[string]json.RawMessage, includeUsage bool) (string, error) {
	chunk := make(map[string]json.RawMessage, len(envelope))
	for key, value := range envelope {
		if key != "obfuscation" && (key != "usage" || includeUsage) {
			chunk[key] = value
		}
	}
	encodedChoices, err := json.Marshal(choices)
	if err != nil {
		return "", err
	}
	chunk["choices"] = encodedChoices
	encoded, err := json.Marshal(chunk)
	return string(encoded), err
}

func ragCitationStreamChunk(envelope map[string]json.RawMessage, index int, annotations []openai.ChatAnnotation) (string, error) {
	chunk := make(map[string]json.RawMessage, len(envelope))
	for key, value := range envelope {
		if key != "choices" && key != "usage" && key != "obfuscation" {
			chunk[key] = value
		}
	}
	choices, err := json.Marshal([]map[string]any{{
		"index": index, "delta": map[string]any{"annotations": annotations}, "finish_reason": nil,
	}})
	if err != nil {
		return "", err
	}
	chunk["choices"] = choices
	encoded, err := json.Marshal(chunk)
	return string(encoded), err
}
