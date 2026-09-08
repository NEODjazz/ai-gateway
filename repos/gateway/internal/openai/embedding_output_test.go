package openai

import (
	"encoding/json"
	"testing"
)

func TestEmbeddingJSONFormats(t *testing.T) {
	for _, test := range []struct {
		name      string
		payload   string
		wantFloat int
		wantBase  string
	}{
		{name: "float", payload: `{"object":"embedding","embedding":[0.25,-1],"index":2}`, wantFloat: 2},
		{name: "base64", payload: `{"object":"embedding","embedding":"AACAPwAAAEA=","index":2}`, wantBase: "AACAPwAAAEA="},
	} {
		t.Run(test.name, func(t *testing.T) {
			var embedding Embedding
			if err := json.Unmarshal([]byte(test.payload), &embedding); err != nil {
				t.Fatal(err)
			}
			if len(embedding.Embedding) != test.wantFloat || embedding.EmbeddingBase64 != test.wantBase {
				t.Fatalf("unexpected decoded embedding: %+v", embedding)
			}
			encoded, err := json.Marshal(embedding)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != test.payload {
				t.Fatalf("embedding format changed: %s", encoded)
			}
		})
	}
}

func TestEmbeddingJSONRequiresValue(t *testing.T) {
	var embedding Embedding
	if err := json.Unmarshal([]byte(`{"object":"embedding","index":0}`), &embedding); err == nil {
		t.Fatal("missing embedding value accepted")
	}
}
