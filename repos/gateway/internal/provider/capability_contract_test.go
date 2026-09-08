package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modelcatalog"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestCatalogCannotExpandDeploymentCapabilities(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","models":[{"provider":"endpoint","model":"m","capabilities":["chat","stream","embeddings","tools"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := Endpoint{Name: "endpoint", Type: "openai-compatible", Capabilities: []string{"chat"}}
	for _, capability := range []string{"stream", "embeddings", "tools"} {
		if supportsCatalogCapabilities(catalog, endpoint, "m", "chat", capability) {
			t.Fatalf("catalog bypassed deployment capability %s", capability)
		}
	}
	if !supportsCatalogCapabilities(catalog, endpoint, "m", "chat") {
		t.Fatal("supported chat rejected")
	}
	endpoint.Capabilities = nil
	if !supportsCatalogCapabilities(catalog, endpoint, "m", "embeddings") {
		t.Fatal("legacy endpoint lost catalog capability")
	}
}
func TestRouterSkipsNativeUnsupportedResponseProtocol(t *testing.T) {
	nativeCalls, compatibleCalls := 0, 0
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { nativeCalls++; w.WriteHeader(500) }))
	defer native.Close()
	compatible := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		compatibleCalls++
		_, _ = fmt.Fprint(w, `{"id":"resp-ok","object":"response","model":"m","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}`)
	}))
	defer compatible.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{
		{Name: "native", Type: "gemini", BaseURL: native.URL, Models: []string{"m"}, Capabilities: []string{"responses"}, Priority: 1},
		{Name: "compatible", Type: "openai-compatible", BaseURL: compatible.URL, Models: []string{"m"}, Capabilities: []string{"responses"}, Priority: 2},
	}})
	request := openai.ResponseRequest{Model: "m", Input: "hello"}
	response, err := router.Responses(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "m"}, ResponseRequest: &request})
	if err != nil || response.ID != "resp-ok" || nativeCalls != 0 || compatibleCalls != 1 {
		t.Fatalf("unsupported adapter blocked valid route: native=%d compatible=%d err=%v", nativeCalls, compatibleCalls, err)
	}
}
