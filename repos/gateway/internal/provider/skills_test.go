package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
)

func TestAnthropicSkillsTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/skills/skill_a/versions" || r.URL.RawQuery != "page=next" {
			t.Fatalf("request=%s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("x-api-key") != "secret" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Content-Type") != "multipart/form-data; boundary=test" {
			t.Fatalf("headers=%v", r.Header)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "payload" {
			t.Fatalf("body=%q err=%v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"version_a","type":"skill_version"}`))
	}))
	defer server.Close()
	response, err := NewAnthropic(server.URL, "secret", false).ExecuteSkillRequest(t.Context(), SkillRequest{
		Method: http.MethodPost, Path: "skills/skill_a/versions", RawQuery: "page=next", ContentType: "multipart/form-data; boundary=test", Body: []byte("payload"),
	})
	if err != nil || response.StatusCode != http.StatusOK || response.ContentType != "application/json" || !strings.Contains(string(response.Body), "version_a") {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestSkillsTransportRejectsUnlistedShapes(t *testing.T) {
	client := NewAnthropic("http://unused.invalid", "", false)
	requests := []SkillRequest{
		{Method: http.MethodPut, Path: "skills"},
		{Method: http.MethodGet, Path: "models"},
		{Method: http.MethodGet, Path: "skills/../models"},
		{Method: http.MethodPost, Path: "skills/skill_a"},
		{Method: http.MethodDelete, Path: "skills/skill_a/versions"},
		{Method: http.MethodGet, Path: "skills/skill_a/versions/v1/archive"},
		{Method: http.MethodGet, Path: "skills", Body: make([]byte, MaxSkillRequestBytes+1)},
	}
	for _, request := range requests {
		if _, err := client.ExecuteSkillRequest(context.Background(), request); err == nil {
			t.Fatalf("invalid request accepted: %+v", request)
		}
	}
}

func TestRouterSkillsRequiresExplicitNativeCapability(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[],"has_more":false}`))
	}))
	defer server.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{
		{Name: "without-skills", Type: "anthropic", BaseURL: server.URL, Models: []string{"m"}, Capabilities: []string{"chat"}},
		{Name: "with-skills", Type: "anthropic", BaseURL: server.URL, Models: []string{"m"}, Capabilities: []string{"skills"}},
	}})
	skills := router.(SkillProvider)
	response, endpoint, err := skills.ExecuteSkillRequest(t.Context(), modules.RequestContext{}, "", SkillRequest{Method: http.MethodGet, Path: "skills"})
	if err != nil || endpoint != "with-skills" || response.StatusCode != http.StatusOK {
		t.Fatalf("endpoint=%q response=%+v err=%v", endpoint, response, err)
	}
	if _, _, err := skills.ExecuteSkillRequest(t.Context(), modules.RequestContext{}, "without-skills", SkillRequest{Method: http.MethodGet, Path: "skills"}); err == nil {
		t.Fatal("endpoint without capability accepted")
	}
}
