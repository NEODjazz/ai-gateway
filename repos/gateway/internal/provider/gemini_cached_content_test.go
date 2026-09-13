package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

const cachedContentResponse = `{"name":"cachedContents/cache-1","displayName":"reference","model":"models/gemini-test","createTime":"2026-09-13T00:00:00Z","updateTime":"2026-09-13T00:00:01Z","expireTime":"2026-09-13T01:00:00Z","usageMetadata":{"totalTokenCount":4096}}`

func TestGeminiCachedContentLifecycleWireContract(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("x-goog-api-key") != "secret" {
			t.Fatal("missing provider authentication")
		}
		switch requests {
		case 1:
			if r.Method != http.MethodPost || r.URL.Path != "/v1beta/cachedContents" || r.URL.RawQuery != "" {
				t.Fatalf("create target: %s %s", r.Method, r.URL.String())
			}
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Fatal("invalid create body")
			}
			encoded, _ := json.Marshal(body)
			for _, want := range []string{`"model":"models/gemini-test"`, `"displayName":"reference"`, `"ttl":"3600s"`, `"contents":[`, `"systemInstruction"`, `"functionDeclarations"`} {
				if !strings.Contains(string(encoded), want) {
					t.Fatalf("create body lost %s: %s", want, encoded)
				}
			}
		case 2:
			if r.Method != http.MethodGet || r.URL.Path != "/v1beta/cachedContents/cache-1" {
				t.Fatalf("get target: %s %s", r.Method, r.URL.String())
			}
		case 3:
			if r.Method != http.MethodPatch || r.URL.Path != "/v1beta/cachedContents/cache-1" || r.URL.Query().Get("updateMask") != "expireTime" {
				t.Fatalf("patch target: %s %s", r.Method, r.URL.String())
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["name"] != "cachedContents/cache-1" || body["expireTime"] != "2026-09-14T00:00:00Z" || len(body) != 2 {
				t.Fatalf("patch body: %#v", body)
			}
		case 4:
			if r.Method != http.MethodDelete || r.URL.Path != "/v1beta/cachedContents/cache-1" {
				t.Fatalf("delete target: %s %s", r.Method, r.URL.String())
			}
			w.WriteHeader(http.StatusNoContent)
			return
		default:
			t.Fatalf("unexpected request %d", requests)
		}
		_, _ = fmt.Fprint(w, cachedContentResponse)
	}))
	defer server.Close()

	client := NewGemini(server.URL, "secret", false)
	request := openai.ChatCompletionRequest{Model: "gemini-test", Messages: []openai.Message{{Role: "system", Content: "reference rules"}, {Role: "user", Content: "reference data"}}, Tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "lookup", Parameters: map[string]any{"type": "object"}}}}}
	created, err := client.CreateCachedContent(t.Context(), request, "reference", openai.GeminiCachedContentExpiration{TTL: "3600s"})
	if err != nil || created.Name != "cachedContents/cache-1" || created.UsageMetadata == nil || created.UsageMetadata.TotalTokenCount != 4096 {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if _, err = client.GetCachedContent(t.Context(), created.Name); err != nil {
		t.Fatal(err)
	}
	if _, err = client.UpdateCachedContent(t.Context(), created.Name, openai.GeminiCachedContentExpiration{ExpireTime: "2026-09-14T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err = client.DeleteCachedContent(t.Context(), created.Name); err != nil || requests != 4 {
		t.Fatalf("delete err=%v requests=%d", err, requests)
	}
}

func TestGeminiCachedContentRejectsInvalidContracts(t *testing.T) {
	client := NewGemini("https://example.invalid", "secret", false)
	base := openai.ChatCompletionRequest{Model: "gemini-test", Messages: []openai.Message{{Role: "user", Content: "reference"}}}
	invalidCreate := []struct {
		request    openai.ChatCompletionRequest
		display    string
		expiration openai.GeminiCachedContentExpiration
	}{
		{base, strings.Repeat("x", 129), openai.GeminiCachedContentExpiration{TTL: "3600s"}},
		{base, "ok", openai.GeminiCachedContentExpiration{}},
		{base, "ok", openai.GeminiCachedContentExpiration{TTL: "3600s", ExpireTime: "2026-09-14T00:00:00Z"}},
		{base, "ok", openai.GeminiCachedContentExpiration{TTL: "0s"}},
		{base, "ok", openai.GeminiCachedContentExpiration{TTL: "1h"}},
		{func() openai.ChatCompletionRequest { value := base; value.Stream = true; return value }(), "ok", openai.GeminiCachedContentExpiration{TTL: "3600s"}},
	}
	for _, test := range invalidCreate {
		if _, err := client.CreateCachedContent(t.Context(), test.request, test.display, test.expiration); err == nil {
			t.Fatalf("invalid create accepted: %+v", test)
		}
	}
	for _, name := range []string{"", "cache-1", "cachedContents/../one", "cachedContents/a/b"} {
		if _, err := client.GetCachedContent(t.Context(), name); err == nil {
			t.Fatalf("invalid name accepted: %q", name)
		}
	}
}

func TestGeminiCachedContentRejectsMalformedMetadata(t *testing.T) {
	for _, response := range []string{
		`{}`,
		strings.Replace(cachedContentResponse, `"totalTokenCount":4096`, `"totalTokenCount":-1`, 1),
		strings.Replace(cachedContentResponse, `"expireTime":"2026-09-13T01:00:00Z"`, `"expireTime":"later"`, 1),
		strings.Replace(cachedContentResponse, `cachedContents/cache-1`, `cachedContents/other`, 1),
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, response) }))
		_, err := NewGemini(server.URL, "secret", false).GetCachedContent(t.Context(), "cachedContents/cache-1")
		server.Close()
		if err == nil {
			t.Fatalf("invalid metadata accepted: %s", response)
		}
	}
}
