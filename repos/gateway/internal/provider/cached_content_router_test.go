package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestCachedContentRouterRequiresCapabilityAppliesAliasAndPinsLifecycle(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("x-goog-api-key") != "secret" {
			t.Fatal("missing provider credential")
		}
		if calls == 1 {
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != "models/upstream-model" {
				t.Fatalf("create body=%#v", body)
			}
			encoded, _ := json.Marshal(body["contents"])
			if !strings.Contains(string(encoded), "masked") || strings.Contains(string(encoded), "sensitive") {
				t.Fatalf("policy transform not applied: %s", encoded)
			}
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = fmt.Fprint(w, `{"name":"cachedContents/cache-1","displayName":"reference","model":"models/upstream-model","createTime":"2026-09-13T00:00:00Z","updateTime":"2026-09-13T00:00:01Z","expireTime":"2026-09-13T01:00:00Z","usageMetadata":{"totalTokenCount":4096}}`)
	}))
	defer server.Close()

	newRuntime := func(capabilities []string) CachedContentProvider {
		return New(Config{Endpoints: []config.ProviderEndpointConfig{{
			Name: "gemini-primary", Type: "gemini", BaseURL: server.URL, APIKey: "secret",
			Models: []string{"public-model"}, ModelAliases: map[string]string{"public-model": "upstream-model"}, Capabilities: capabilities,
		}}}).(CachedContentProvider)
	}
	request := openai.ChatCompletionRequest{Model: "public-model", Messages: []openai.Message{{Role: "user", Content: "sensitive"}}}
	expiration := openai.GeminiCachedContentExpiration{TTL: "3600s"}
	if _, _, err := newRuntime([]string{"chat"}).CreateCachedContent(t.Context(), modules.RequestContext{}, request, "reference", expiration, nil); err == nil {
		t.Fatal("cached content route accepted without explicit capability")
	}
	runtime := newRuntime([]string{"chat", "cached_content"})
	var attempt *modules.RequestContext
	created, binding, err := runtime.CreateCachedContent(t.Context(), modules.RequestContext{}, request, "reference", expiration, func(_ context.Context, current *modules.RequestContext) error {
		attempt = current
		current.Request.Messages[0].Content = "masked"
		return nil
	})
	if err != nil || created.Name != "cachedContents/cache-1" || binding.Endpoint != "gemini-primary" || binding.Model != "public-model" || len(binding.Deployment) != 64 || len(binding.Policy) != 64 {
		t.Fatalf("created=%+v binding=%+v err=%v", created, binding, err)
	}
	if attempt == nil || attempt.Request.Model != "upstream-model" || attempt.Metadata["gateway.api_type"] != "cached_content" || attempt.Response == nil || attempt.Response.Usage.PromptTokens != 4096 || attempt.Response.Usage.PromptTokensDetails == nil || attempt.Response.Usage.PromptTokensDetails.CacheWriteTokens != 4096 {
		t.Fatalf("attempt=%+v", attempt)
	}
	if _, err = runtime.RetrieveCachedContent(t.Context(), binding, created.Name); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.UpdateCachedContent(t.Context(), binding, created.Name, openai.GeminiCachedContentExpiration{TTL: "7200s"}); err != nil {
		t.Fatal(err)
	}
	if err = runtime.DeleteCachedContent(t.Context(), binding, created.Name); err != nil {
		t.Fatal(err)
	}
	changed := binding
	changed.Deployment = strings.Repeat("0", 64)
	if _, err = runtime.RetrieveCachedContent(t.Context(), changed, created.Name); !errors.Is(err, ErrCachedContentDeploymentChanged) {
		t.Fatalf("changed deployment err=%v", err)
	}
}

func TestCachedContentPolicyIdentityIsDeterministicAndPolicyScoped(t *testing.T) {
	first := modules.RequestContext{Metadata: map[string]string{
		"provider.modules.anonymizer.rules": "email,phone",
		"provider.modules.dlp.enabled":      "true",
		"provider.id":                       "ignored",
	}}
	second := modules.RequestContext{Metadata: map[string]string{
		"provider.id":                       "different-but-ignored",
		"provider.modules.dlp.enabled":      "true",
		"provider.modules.anonymizer.rules": "email,phone",
	}}
	if cachedContentPolicyIdentity(first) != cachedContentPolicyIdentity(second) {
		t.Fatal("equivalent effective policies produced different identities")
	}
	second.Metadata["provider.modules.anonymizer.rules"] = "email"
	if cachedContentPolicyIdentity(first) == cachedContentPolicyIdentity(second) {
		t.Fatal("policy change did not change identity")
	}
}

func TestCachedContentRouterDeletesResourceWhenUsageIsMissing(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1beta/cachedContents/cache-1" {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = fmt.Fprint(w, `{"name":"cachedContents/cache-1","model":"models/upstream-model","createTime":"2026-09-13T00:00:00Z","updateTime":"2026-09-13T00:00:01Z","expireTime":"2026-09-13T01:00:00Z"}`)
	}))
	defer server.Close()
	runtime := New(Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "gemini-primary", Type: "gemini", BaseURL: server.URL, APIKey: "secret", Models: []string{"public-model"},
		ModelAliases: map[string]string{"public-model": "upstream-model"}, Capabilities: []string{"chat", "cached_content"},
	}}}).(CachedContentProvider)
	request := openai.ChatCompletionRequest{Model: "public-model", Messages: []openai.Message{{Role: "user", Content: "reference"}}}
	if _, _, err := runtime.CreateCachedContent(t.Context(), modules.RequestContext{}, request, "", openai.GeminiCachedContentExpiration{TTL: "3600s"}, nil); err == nil || !deleted {
		t.Fatalf("err=%v deleted=%t", err, deleted)
	}
}
