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

func TestGeminiManagedToolsRequireExplicitEndpointCapabilities(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","models":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	legacy := Endpoint{Name: "gemini", Type: "gemini", Provider: Gemini{}}
	for _, capability := range []string{"gemini_code_execution", "url_context", "google_maps"} {
		if supportsCatalogCapabilities(catalog, legacy, "model", "chat", capability) {
			t.Fatalf("legacy endpoint implicitly enabled %s", capability)
		}
		explicit := legacy
		explicit.Capabilities = []string{"chat", capability}
		if !supportsCatalogCapabilities(catalog, explicit, "model", "chat", capability) {
			t.Fatalf("explicit endpoint rejected %s", capability)
		}
	}
	if supportsCatalogCapabilities(catalog, Endpoint{Name: "gemini", Type: "gemini", Provider: Gemini{}, Capabilities: []string{"chat", "audio_input", "gemini_audio_timestamp"}}, "model", "chat", "gemini_audio_timestamp") {
		t.Fatal("public Gemini endpoint enabled Vertex-only audio timestamps")
	}
	vertex := Endpoint{Name: "vertex", Type: "vertex-gemini", Provider: NewVertexGemini("https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google", false), Capabilities: []string{"chat", "audio_input", "gemini_audio_timestamp"}}
	if !supportsCatalogCapabilities(catalog, vertex, "model", "chat", "gemini_audio_timestamp") {
		t.Fatal("explicit Vertex audio timestamp capability rejected")
	}
}

func TestGeminiMediaResolutionRequiresExplicitNativeCapability(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","models":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []Endpoint{
		{Name: "gemini", Type: "gemini", Provider: Gemini{}},
		{Name: "vertex", Type: "vertex-gemini", Provider: NewVertexGemini("https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google", false)},
	} {
		if supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "vision", "gemini_media_resolution") {
			t.Fatalf("legacy endpoint implicitly enabled media resolution: %s", endpoint.Type)
		}
		endpoint.Capabilities = []string{"chat", "vision", "gemini_media_resolution"}
		if !supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "vision", "gemini_media_resolution") {
			t.Fatalf("explicit native endpoint rejected media resolution: %s", endpoint.Type)
		}
	}
}

func TestGeminiMediaProcessingRequiresExplicitNativeCapability(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","models":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []Endpoint{
		{Name: "gemini", Type: "gemini", Provider: Gemini{}},
		{Name: "vertex", Type: "vertex-gemini", Provider: NewVertexGemini("https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google", false)},
	} {
		if supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "video_input", "gemini_media_processing") {
			t.Fatalf("legacy endpoint implicitly enabled media processing: %s", endpoint.Type)
		}
		endpoint.Capabilities = []string{"chat", "video_input", "gemini_media_processing"}
		if !supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "video_input", "gemini_media_processing") {
			t.Fatalf("explicit native endpoint rejected media processing: %s", endpoint.Type)
		}
	}
}

func TestGeminiSearchTimeRangeRequiresExplicitNativeCapability(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","models":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []Endpoint{
		{Name: "gemini", Type: "gemini", Provider: Gemini{}},
		{Name: "vertex", Type: "vertex-gemini", Provider: NewVertexGemini("https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google", false)},
	} {
		if supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "web_search", "gemini_search_time_range") {
			t.Fatalf("legacy endpoint implicitly enabled search time range: %s", endpoint.Type)
		}
		endpoint.Capabilities = []string{"chat", "web_search", "gemini_search_time_range"}
		if !supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "web_search", "gemini_search_time_range") {
			t.Fatalf("explicit native endpoint rejected search time range: %s", endpoint.Type)
		}
	}
}

func TestGeminiFileSearchRequiresExplicitNativeCapability(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","models":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []Endpoint{
		{Name: "gemini", Type: "gemini", Provider: Gemini{}},
		{Name: "vertex", Type: "vertex-gemini", Provider: NewVertexGemini("https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google", false)},
	} {
		if supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "gemini_file_search") {
			t.Fatalf("legacy endpoint implicitly enabled file search: %s", endpoint.Type)
		}
		endpoint.Capabilities = []string{"chat", "gemini_file_search"}
		if !supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "gemini_file_search") {
			t.Fatalf("explicit native endpoint rejected file search: %s", endpoint.Type)
		}
	}
}

func TestGeminiComputerUseRequiresExplicitNativeCapability(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","models":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []Endpoint{{Name: "gemini", Type: "gemini", Provider: Gemini{}}, {Name: "vertex", Type: "vertex-gemini", Provider: NewVertexGemini("https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google", false)}} {
		if supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "gemini_computer_use") {
			t.Fatalf("legacy endpoint implicitly enabled computer use: %s", endpoint.Type)
		}
		endpoint.Capabilities = []string{"chat", "gemini_computer_use"}
		if !supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "gemini_computer_use") {
			t.Fatalf("explicit native endpoint rejected computer use: %s", endpoint.Type)
		}
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
