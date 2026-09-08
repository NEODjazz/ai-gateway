package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestResponsesRejectsInvalidJSONDocuments(t *testing.T) {
	for _, body := range []string{"null", `{"id":"r"} {"id":"second"}`, `{"id":"r"} trailing`, `{"id":`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, body)
			}))
			defer server.Close()
			client := NewOpenAICompatible(server.URL, "", false)
			if _, err := client.Responses(context.Background(), openai.ResponseRequest{Model: "m", Input: "hello"}); err == nil {
				t.Fatal("invalid JSON document accepted")
			}
		})
	}
}

func TestResponsesJSONReadLimitAndErrors(t *testing.T) {
	reader := &embeddingLimitReader{}
	if _, err := decodeResponseJSON(reader); err == nil || reader.read != maxResponseJSONBytes+1 {
		t.Fatalf("read=%d err=%v", reader.read, err)
	}
	failure := errors.New("read interrupted")
	if _, err := decodeResponseJSON(embeddingErrorReader{failure}); !errors.Is(err, failure) {
		t.Fatalf("I/O error lost: %v", err)
	}
	document := `{"id":"r","object":"response","model":"m","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`
	padded := document + strings.Repeat(" ", maxResponseJSONBytes-len(document))
	response, err := decodeResponseJSON(strings.NewReader(padded))
	if err != nil || response.ID != "r" || response.OutputText != "hello" || response.Usage.TotalTokens != 3 {
		t.Fatalf("valid boundary: response=%+v err=%v", response, err)
	}
}

func TestResponsesRejectsOversizedHTTPBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"id":"r"}`); err != nil {
			return
		}
		_, _ = io.CopyN(w, &embeddingLimitReader{}, maxResponseJSONBytes)
	}))
	defer server.Close()
	client := NewOpenAICompatible(server.URL, "", false)
	if _, err := client.Responses(context.Background(), openai.ResponseRequest{Model: "m", Input: "hello"}); err == nil {
		t.Fatal("oversized HTTP response accepted")
	}
}
