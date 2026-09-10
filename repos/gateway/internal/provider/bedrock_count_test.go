package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/openai"
)

func TestBedrockCountTokensUsesConverseInputAndSigV4(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/model/us.anthropic.claude-v1:0/count-tokens" || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKID/20260102/us-east-1/bedrock/aws4_request") || r.Header.Get("X-Amz-Content-Sha256") == "" {
			t.Fatalf("path=%q headers=%v", r.URL.EscapedPath(), r.Header)
		}
		var body struct {
			Input struct {
				Converse bedrockRequest `json:"converse"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Input.Converse.Messages) != 1 || len(body.Input.Converse.System) != 1 || body.Input.Converse.ToolConfig == nil || len(body.Input.Converse.ToolConfig.Tools) != 1 {
			t.Fatalf("request=%+v", body.Input.Converse)
		}
		_, _ = fmt.Fprint(w, `{"inputTokens":456}`)
	}))
	defer server.Close()
	client := NewBedrockWithAuth(server.URL, `{"access_key_id":"AKID","secret_access_key":"secret"}`, "aws_sigv4", "us-east-1")
	client.now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	request := TokenCountRequest{
		Model:    "us.anthropic.claude-v1:0",
		Messages: []openai.Message{{Role: "system", Content: "be concise"}, {Role: "user", Content: "weather"}},
		Tools:    []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather", Parameters: map[string]any{"type": "object"}}}},
	}
	var counter TokenCountClient = client
	result, err := counter.CountTokens(t.Context(), request)
	if err != nil || result.InputTokens != 456 || result.Model != request.Model || result.Source != "bedrock" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestBedrockCountTokensRejectsInvalidResponses(t *testing.T) {
	for name, body := range map[string]string{
		"missing":   `{}`,
		"negative":  `{"inputTokens":-1}`,
		"oversized": strings.Repeat("x", maxBedrockTokenCountResponseBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, body) }))
			defer server.Close()
			request := TokenCountRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}
			if _, err := NewBedrock(server.URL, "key").CountTokens(t.Context(), request); err == nil {
				t.Fatal("invalid token count response accepted")
			}
		})
	}
}

func TestBedrockCountTokensRejectsRedirect(t *testing.T) {
	reached := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	request := TokenCountRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}
	if _, err := NewBedrock(redirect.URL, "key").CountTokens(t.Context(), request); err == nil || reached {
		t.Fatalf("redirect followed: err=%v reached=%v", err, reached)
	}
}
