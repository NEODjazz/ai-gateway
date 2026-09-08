package provider

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

type embeddingLimitReader struct{ read int }

func (r *embeddingLimitReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	r.read += len(p)
	return len(p), nil
}

type embeddingErrorReader struct{ err error }

func (r embeddingErrorReader) Read([]byte) (int, error) { return 0, r.err }
func TestEmbeddingResponseDecodeBounds(t *testing.T) {
	reader := &embeddingLimitReader{}
	var target any
	if err := decodeEmbeddingResponse(reader, &target); err == nil || reader.read != maxEmbeddingResponseBytes+1 {
		t.Fatalf("unbounded read: bytes=%d err=%v", reader.read, err)
	}
	failure := errors.New("read interrupted")
	if err := decodeEmbeddingResponse(embeddingErrorReader{failure}, &target); !errors.Is(err, failure) {
		t.Fatal("read error lost")
	}
	if err := decodeEmbeddingResponse(strings.NewReader(`{} {}`), &target); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	if err := decodeEmbeddingResponse(strings.NewReader(`{"usage":`), &target); err == nil {
		t.Fatal("truncated JSON accepted")
	}
}
func TestEmbeddingVectorValidation(t *testing.T) {
	dimensions := 2
	request := openai.EmbeddingRequest{Input: []string{"a", "b"}, Dimensions: &dimensions}
	valid := []openai.Embedding{{Index: 1, Embedding: []float64{1, 2}}, {Index: 0, Embedding: []float64{3, 4}}}
	if err := validateEmbeddingVectors(request, valid); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]openai.Embedding{
		nil,
		{{Index: 0, Embedding: []float64{1, 2}}},
		{{Index: 0, Embedding: []float64{1, 2}}, {Index: 0, Embedding: []float64{3, 4}}},
		{{Index: -1, Embedding: []float64{1, 2}}, {Index: 1, Embedding: []float64{3, 4}}},
		{{Index: 0, Embedding: []float64{1, 2}}, {Index: 2, Embedding: []float64{3, 4}}},
		{{Index: 0, Embedding: nil}, {Index: 1, Embedding: nil}},
		{{Index: 0, Embedding: []float64{1}}, {Index: 1, Embedding: []float64{2}}},
		{{Index: 0, Embedding: []float64{1, 2}}, {Index: 1, Embedding: []float64{3}}},
		{{Index: 0, Embedding: []float64{math.Inf(1), 2}}, {Index: 1, Embedding: []float64{3, 4}}},
	} {
		if err := validateEmbeddingVectors(request, data); err == nil {
			t.Fatal("malformed vectors accepted")
		}
	}
	if err := validateEmbeddingVectors(openai.EmbeddingRequest{Input: "a"}, []openai.Embedding{{Embedding: make([]float64, 65537)}}); err == nil {
		t.Fatal("dimension limit ignored")
	}
	if err := validateEmbeddingVectors(openai.EmbeddingRequest{Input: []any{[]any{1.0}, []any{2.0}}}, valid); err != nil {
		t.Fatalf("token-array input count was not honored: %v", err)
	}
}

func TestBase64EmbeddingVectorValidation(t *testing.T) {
	encode := func(values ...uint32) string {
		data := make([]byte, len(values)*4)
		for index, value := range values {
			binary.LittleEndian.PutUint32(data[index*4:], value)
		}
		return base64.StdEncoding.EncodeToString(data)
	}
	dimensions := 2
	request := openai.EmbeddingRequest{Input: "a", EncodingFormat: "base64", Dimensions: &dimensions}
	valid := encode(math.Float32bits(1), math.Float32bits(-2.5))
	if err := validateEmbeddingVectors(request, []openai.Embedding{{EmbeddingBase64: valid}}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []openai.Embedding{
		{Embedding: []float64{1, 2}},
		{EmbeddingBase64: "%%%"},
		{EmbeddingBase64: base64.StdEncoding.EncodeToString([]byte{1, 2, 3})},
		{EmbeddingBase64: encode(math.Float32bits(float32(math.Inf(1))), math.Float32bits(1))},
		{EmbeddingBase64: encode(math.Float32bits(1))},
	} {
		if err := validateEmbeddingVectors(request, []openai.Embedding{value}); err == nil {
			t.Fatalf("invalid base64 embedding accepted: %+v", value)
		}
	}
	if err := validateEmbeddingVectors(openai.EmbeddingRequest{Input: "a"}, []openai.Embedding{{EmbeddingBase64: valid}}); err == nil {
		t.Fatal("base64 response accepted for float request")
	}
}
func TestEmbeddingAdaptersEnforceResponseContract(t *testing.T) {
	for _, adapter := range []string{"compatible", "ollama"} {
		for _, mode := range []string{"empty", "dimension", "trailing", "oversized"} {
			t.Run(adapter+"/"+mode, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body := `{"data":[{"index":0,"embedding":[1]}]}`
					if adapter == "ollama" {
						body = `{"embeddings":[[1]]}`
					}
					switch mode {
					case "empty":
						body = `{}`
					case "trailing":
						body += ` {}`
					}
					_, _ = fmt.Fprint(w, body)
					if mode == "oversized" {
						_, _ = io.CopyN(w, &embeddingLimitReader{}, maxEmbeddingResponseBytes)
					}
				}))
				defer server.Close()
				var client EmbeddingClient = NewOpenAICompatible(server.URL, "", false)
				if adapter == "ollama" {
					client = NewOllama(server.URL, false)
				}
				dimensions := 2
				_, err := client.Embeddings(context.Background(), openai.EmbeddingRequest{Model: "m", Input: "a", Dimensions: &dimensions})
				if err == nil {
					t.Fatal("malformed upstream response accepted")
				}
				if mode == "oversized" && !strings.Contains(err.Error(), "exceeds limit") {
					t.Fatalf("wrong bound error: %v", err)
				}
			})
		}
	}
}
