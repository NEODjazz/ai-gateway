package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type streamingTextDeanonymizer struct {
	replacements map[string]string
	pending      map[string]string
}

func newStreamingTextDeanonymizer(replacements map[string]string) *streamingTextDeanonymizer {
	return &streamingTextDeanonymizer{replacements: replacements, pending: map[string]string{}}
}

func (d *streamingTextDeanonymizer) consume(key, fragment string) string {
	if d == nil || len(d.replacements) == 0 {
		return fragment
	}
	value := d.pending[key] + fragment
	d.pending[key] = ""
	var output strings.Builder
	for value != "" {
		placeholder, index := d.firstPlaceholder(value)
		if index >= 0 {
			output.WriteString(value[:index])
			output.WriteString(d.replacements[placeholder])
			value = value[index+len(placeholder):]
			continue
		}
		held := d.placeholderPrefixSuffix(value)
		output.WriteString(value[:len(value)-held])
		d.pending[key] = value[len(value)-held:]
		break
	}
	return output.String()
}

func (d *streamingTextDeanonymizer) flush(key string) string {
	value := d.pending[key]
	delete(d.pending, key)
	return value
}

func (d *streamingTextDeanonymizer) firstPlaceholder(value string) (string, int) {
	selected := ""
	selectedIndex := -1
	for placeholder := range d.replacements {
		index := strings.Index(value, placeholder)
		if index >= 0 && (selectedIndex < 0 || index < selectedIndex || index == selectedIndex && len(placeholder) > len(selected)) {
			selected, selectedIndex = placeholder, index
		}
	}
	return selected, selectedIndex
}

func (d *streamingTextDeanonymizer) placeholderPrefixSuffix(value string) int {
	longest := 0
	for placeholder := range d.replacements {
		limit := min(len(value), len(placeholder)-1)
		for size := limit; size > longest; size-- {
			if strings.HasSuffix(value, placeholder[:size]) {
				longest = size
				break
			}
		}
	}
	return longest
}

func deanonymizingChatStreamWriter(replacements map[string]string, write ChatCompletionStreamWriter) ChatCompletionStreamWriter {
	if write == nil || len(replacements) == 0 {
		return write
	}
	deanonymizer := newStreamingTextDeanonymizer(replacements)
	return func(payload string) error {
		decoded, err := decodeJSONObject(payload)
		if err != nil {
			return err
		}
		choices, _ := decoded["choices"].([]any)
		for order, rawChoice := range choices {
			choice, _ := rawChoice.(map[string]any)
			index := jsonInt(choice["index"], order)
			key := fmt.Sprintf("choice:%d:content", index)
			delta, _ := choice["delta"].(map[string]any)
			if content, ok := delta["content"].(string); ok {
				delta["content"] = deanonymizer.consume(key, content)
			}
			refusalKey := fmt.Sprintf("choice:%d:refusal", index)
			if refusal, ok := delta["refusal"].(string); ok {
				delta["refusal"] = deanonymizer.consume(refusalKey, refusal)
			}
			deanonymizeToolCallDeltas(deanonymizer, index, delta)
			if finish, ok := choice["finish_reason"].(string); ok && finish != "" {
				if pending := deanonymizer.flush(key); pending != "" {
					if delta == nil {
						delta = map[string]any{}
						choice["delta"] = delta
					}
					delta["content"] = stringValue(delta["content"]) + pending
				}
				if pending := deanonymizer.flush(refusalKey); pending != "" {
					if delta == nil {
						delta = map[string]any{}
						choice["delta"] = delta
					}
					delta["refusal"] = stringValue(delta["refusal"]) + pending
				}
			}
		}
		return writeJSONPayload(write, decoded)
	}
}

func deanonymizingCompletionStreamWriter(replacements map[string]string, write CompletionStreamWriter) CompletionStreamWriter {
	if write == nil || len(replacements) == 0 {
		return write
	}
	deanonymizer := newStreamingTextDeanonymizer(replacements)
	return func(payload string) error {
		decoded, err := decodeJSONObject(payload)
		if err != nil {
			return err
		}
		choices, _ := decoded["choices"].([]any)
		for order, rawChoice := range choices {
			choice, _ := rawChoice.(map[string]any)
			index := jsonInt(choice["index"], order)
			key := fmt.Sprintf("completion:%d:text", index)
			if content, ok := choice["text"].(string); ok {
				choice["text"] = deanonymizer.consume(key, content)
			}
			if finish, ok := choice["finish_reason"].(string); ok && finish != "" {
				choice["text"] = stringValue(choice["text"]) + deanonymizer.flush(key)
			}
		}
		marshaled, err := json.Marshal(decoded)
		if err != nil {
			return err
		}
		return write(string(marshaled))
	}
}

func deanonymizeToolCallDeltas(deanonymizer *streamingTextDeanonymizer, choiceIndex int, delta map[string]any) {
	if delta == nil {
		return
	}
	calls, _ := delta["tool_calls"].([]any)
	for order, rawCall := range calls {
		call, _ := rawCall.(map[string]any)
		callIndex := jsonInt(call["index"], order)
		function, _ := call["function"].(map[string]any)
		if arguments, ok := function["arguments"].(string); ok {
			key := fmt.Sprintf("choice:%d:tool:%d", choiceIndex, callIndex)
			function["arguments"] = deanonymizer.consume(key, arguments)
		}
	}
}

func deanonymizingResponseStreamWriter(replacements map[string]string, write ResponseStreamWriter) ResponseStreamWriter {
	if write == nil || len(replacements) == 0 {
		return write
	}
	deanonymizer := newStreamingTextDeanonymizer(replacements)
	return func(event, payload string) error {
		decoded, err := decodeJSONObject(payload)
		if err != nil {
			return err
		}
		switch event {
		case "response.output_text.delta":
			if delta, ok := decoded["delta"].(string); ok {
				key := fmt.Sprintf("output:%d:text", jsonInt(decoded["output_index"], 0))
				decoded["delta"] = deanonymizer.consume(key, delta)
			}
		case "response.function_call_arguments.delta":
			if delta, ok := decoded["delta"].(string); ok {
				key := fmt.Sprintf("output:%d:arguments", jsonInt(decoded["output_index"], 0))
				decoded["delta"] = deanonymizer.consume(key, delta)
			}
		default:
			deanonymizeJSONStrings(decoded, replacements)
		}
		marshaled, err := json.Marshal(decoded)
		if err != nil {
			return err
		}
		return write(event, string(marshaled))
	}
}

func decodeJSONObject(payload string) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(payload))
	decoder.UseNumber()
	var decoded map[string]any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func writeJSONPayload(write ChatCompletionStreamWriter, payload map[string]any) error {
	marshaled, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return write(string(marshaled))
}

func deanonymizeJSONStrings(value any, replacements map[string]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if text, ok := child.(string); ok {
				typed[key] = deanonymizeText(text, replacements)
				continue
			}
			deanonymizeJSONStrings(child, replacements)
		}
	case []any:
		for _, child := range typed {
			deanonymizeJSONStrings(child, replacements)
		}
	}
}

func deanonymizeText(value string, replacements map[string]string) string {
	for placeholder, original := range replacements {
		value = strings.ReplaceAll(value, placeholder, original)
	}
	return value
}

func jsonInt(value any, fallback int) int {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return int(parsed)
		}
	case float64:
		return int(typed)
	}
	return fallback
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
