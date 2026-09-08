package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRetrieveResponseTransport(t *testing.T) {
	for _, base := range []string{"", "/v1"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" || r.URL.Path != "/v1/responses/resp_123" || r.Header.Get("Authorization") != "Bearer test-key" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
			fmt.Fprint(w, `{"id":"resp_123","status":"completed","metadata":{"ticket":"42"},"output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`)
		}))
		p := NewOpenAICompatible(server.URL+base, "test-key", true)
		result, err := p.RetrieveResponse(t.Context(), "resp_123")
		server.Close()
		if err != nil || result.OutputText != "answer" || result.Usage.TotalTokens != 5 || result.Metadata["ticket"] != "42" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
}

func TestCancelResponseTransport(t *testing.T) {
	for _, base := range []string{"", "/v1"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/responses/resp_123/cancel" || r.Header.Get("Authorization") != "Bearer test-key" || r.ContentLength > 0 {
				t.Errorf("unexpected request: %s %s content-length=%d", r.Method, r.URL.Path, r.ContentLength)
			}
			fmt.Fprint(w, `{"id":"resp_123","status":"cancelled","output":[],"usage":{"input_tokens":2,"output_tokens":0,"total_tokens":2}}`)
		}))
		result, err := NewOpenAICompatible(server.URL+base, "test-key", true).CancelResponse(t.Context(), "resp_123")
		server.Close()
		if err != nil || result.ID != "resp_123" || result.Status != "cancelled" || result.Usage.TotalTokens != 2 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
}

func TestRetrieveResponseRejectsUnsafeIDs(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	p := NewOpenAICompatible(server.URL, "", true)
	for _, id := range []string{"", "../models", "a/b", "a?x=y", "a#b", "a%2fb", "a b", strings.Repeat("a", 257)} {
		for _, operation := range []func() error{
			func() error { _, err := p.RetrieveResponse(t.Context(), id); return err },
			func() error { _, err := p.CancelResponse(t.Context(), id); return err },
		} {
			var failure *Error
			if err := operation(); !errors.As(err, &failure) || failure.Param != "response_id" || failure.StatusCode != 400 {
				t.Fatalf("id=%q err=%v", id, err)
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unsafe ID reached upstream")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.RetrieveResponse(ctx, "resp_123"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestRetrieveResponseRejectsInvalidResult(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"id":"other"}`, `{"id":"resp_123","usage":{"input_tokens":-1}}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		_, err := NewOpenAICompatible(server.URL, "", true).RetrieveResponse(t.Context(), "resp_123")
		server.Close()
		if err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestRetrieveResponseErrorsAndRedirects(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	for _, status := range []int{302, 404, 429, 503} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", target.URL)
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"code":"not_found","message":"private upstream content"}}`)
		}))
		_, err := NewOpenAICompatible(server.URL, "test-key", true).RetrieveResponse(t.Context(), "resp_123")
		server.Close()
		var failure *Error
		if !errors.As(err, &failure) || failure.StatusCode != status || strings.Contains(err.Error(), "private upstream content") {
			t.Fatalf("status=%d err=%v", status, err)
		}
	}
	if targetCalls.Load() != 0 {
		t.Fatal("redirect followed")
	}
}
