package openai

import (
	"bytes"
	"encoding/json"
	"errors"
)

func (e Embedding) MarshalJSON() ([]byte, error) {
	var value any = e.Embedding
	if e.EmbeddingBase64 != "" {
		value = e.EmbeddingBase64
	}
	return json.Marshal(struct {
		Object    string `json:"object"`
		Embedding any    `json:"embedding"`
		Index     int    `json:"index"`
	}{Object: e.Object, Embedding: value, Index: e.Index})
}

func (e *Embedding) UnmarshalJSON(payload []byte) error {
	var wire struct {
		Object    string          `json:"object"`
		Embedding json.RawMessage `json:"embedding"`
		Index     int             `json:"index"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		return err
	}
	raw := bytes.TrimSpace(wire.Embedding)
	if len(raw) == 0 {
		return errors.New("embedding value is required")
	}
	e.Object = wire.Object
	e.Index = wire.Index
	e.Embedding = nil
	e.EmbeddingBase64 = ""
	if raw[0] == '"' {
		return json.Unmarshal(raw, &e.EmbeddingBase64)
	}
	return json.Unmarshal(raw, &e.Embedding)
}
