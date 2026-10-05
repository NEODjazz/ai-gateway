package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// Missing usage can be estimated. A present usage object must contain complete,
// consistent counts; subtraction avoids overflowing while checking the total.
func validateLemonadeTokenUsage(payload []byte, responses bool) error {
	var envelope struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	if len(envelope.Usage) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Usage), []byte("null")) {
		return nil
	}
	var counts struct {
		Prompt     *int `json:"prompt_tokens"`
		Completion *int `json:"completion_tokens"`
		Input      *int `json:"input_tokens"`
		Output     *int `json:"output_tokens"`
		Total      *int `json:"total_tokens"`
	}
	if err := json.Unmarshal(envelope.Usage, &counts); err != nil {
		return err
	}
	a, b, total := counts.Prompt, counts.Completion, counts.Total
	if responses {
		a, b = counts.Input, counts.Output
	}
	if a == nil || b == nil || total == nil || *a < 0 || *b < 0 || *total < *a || *total-*a != *b {
		return errors.New("Lemonade returned invalid or incomplete token usage")
	}
	return nil
}

func lemonadeUsageReader(reader io.Reader, responses bool) (io.Reader, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxResponseJSONBytes+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > maxResponseJSONBytes {
		return nil, errors.New("Lemonade response exceeds limit")
	}
	if err := validateLemonadeTokenUsage(payload, responses); err != nil {
		return nil, err
	}
	return bytes.NewReader(payload), nil
}

func lemonadeChatStreamUsage(payload string) (string, error) {
	return payload, validateLemonadeTokenUsage([]byte(payload), false)
}

func validateLemonadeResponseEvent(payload string) error {
	var envelope struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		return err
	}
	if len(envelope.Response) == 0 {
		return nil
	}
	return validateLemonadeTokenUsage(envelope.Response, true)
}
