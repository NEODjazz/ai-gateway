package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGeminiCounterIncludesSystemToolsAndImages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-test:countTokens" || r.URL.RawQuery != "" || r.Header.Get("x-goog-api-key") != "test-key" || r.Header.Get("Authorization") != "" {
			t.Error("wrong native transport")
		}
		var body struct {
			Request struct {
				Model string `json:"model"`
				geminiRequest
			} `json:"generateContentRequest"`
			Contents json.RawMessage `json:"contents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		native := body.Request
		if len(body.Contents) != 0 || native.Model != "models/gemini-test" || native.System == nil || native.System.Parts[0].Text != "Be concise" || len(native.Tools) != 1 || native.Tools[0].Functions[0].Name != "weather" {
			t.Errorf("full context lost: %+v", body)
		}
		if len(native.Contents) != 1 || len(native.Contents[0].Parts) != 2 || native.Contents[0].Parts[1].InlineData == nil || native.Generation.MediaResolution != "MEDIA_RESOLUTION_HIGH" {
			t.Error("inline image lost")
		}
		if native.Generation.MaxOutputTokens != nil {
			t.Error("counter has generation budget")
		}
		_, _ = w.Write([]byte(`{"totalTokens":456,"cachedContentTokenCount":12}`))
	}))
	defer server.Close()
	request := countTestRequest()
	request.Model = "models/gemini-test"
	request.GeminiMediaResolution = "MEDIA_RESOLUTION_HIGH"
	var counter TokenCountClient = NewGemini(server.URL, "test-key", false)
	result, err := counter.CountTokens(context.Background(), request)
	if err != nil || result.InputTokens != 456 || result.Source != "gemini" || result.Model != request.Model {
		t.Fatalf("count: %+v %v", result, err)
	}
}
func TestGeminiCounterRejectsInvalidCountsAndRequests(t *testing.T) {
	for _, payload := range []string{`{}`, `{"totalTokens":null}`, `{"totalTokens":-1}`, `{"totalTokens":1.5}`, `{"totalTokens":9223372036854775808}`, strings.Repeat(" ", (64<<10)+1)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(payload)) }))
		_, err := NewGemini(server.URL, "", false).CountTokens(context.Background(), countTestRequest())
		server.Close()
		if err == nil {
			t.Fatal("invalid count accepted")
		}
	}
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	request := countTestRequest()
	request.Model = "../other"
	if _, err := NewGemini(server.URL, "", false).CountTokens(context.Background(), request); err == nil {
		t.Fatal("invalid model accepted")
	}
	request = countTestRequest()
	parallel := false
	request.ParallelToolCalls = &parallel
	if _, err := NewGemini(server.URL, "", false).CountTokens(context.Background(), request); err == nil {
		t.Fatal("unsupported parallel control accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input reached HTTP")
	}
}
func TestGeminiCounterRejectsRedirectAndHonorsCancellation(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redirect.Close()
	if _, err := NewGemini(redirect.URL, "test-key", false).CountTokens(context.Background(), countTestRequest()); err == nil || reached.Load() {
		t.Fatal("redirect accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		cancel()
		<-r.Context().Done()
	}))
	defer server.Close()
	if _, err := NewGemini(server.URL, "", false).CountTokens(ctx, countTestRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
