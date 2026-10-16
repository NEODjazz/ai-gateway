package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
)

func TestJWTDirectoryEmptyGrantsDenyModelsThroughRemoteAuth(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy empty grants", true: "directory empty grants"}[restricted], func(t *testing.T) {
			auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if err := json.NewEncoder(w).Encode(modules.AuthResponse{UserID: "user-a", CredentialID: "jwt:principal-a", ModelAccessRestricted: restricted, ToolAccessRestricted: restricted}); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(auth.Close)
			pipeline := modules.NewPipeline([]modules.Module{modules.NewRemoteAuthModule(true, auth.URL)})
			handler := Routes(NewHandler(pipeline, modelsProvider{}))
			request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			request.Header.Set("Authorization", "Bearer test-token")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("model listing failed: %d", response.Code)
			}
			var result struct {
				Data []any `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if restricted && len(result.Data) != 0 || !restricted && len(result.Data) == 0 {
				t.Fatalf("model-list restriction was lost: restricted=%v count=%d", restricted, len(result.Data))
			}
			request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.3","messages":[{"role":"user","content":"hello"}]}`))
			request.Header.Set("Authorization", "Bearer test-token")
			response = httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if restricted && response.Code != http.StatusForbidden {
				t.Fatalf("direct model request bypassed restriction: %d", response.Code)
			}
		})
	}
}

func TestJWTDirectoryEmptyToolGrantsDenyTools(t *testing.T) {
	req := modules.RequestContext{ToolAccessRestricted: true}
	if (Handler{}).authorizeTools(httptest.NewRecorder(), req, []string{"read"}, true) {
		t.Fatal("empty explicit tool grants allowed a tool")
	}
	if !(Handler{}).authorizeTools(httptest.NewRecorder(), req, nil, true) {
		t.Fatal("tool-free request was rejected")
	}
	req.ToolAccessRestricted = false
	if !(Handler{}).authorizeTools(httptest.NewRecorder(), req, []string{"read"}, true) {
		t.Fatal("legacy tool grant semantics changed")
	}
}

func TestJWTPrincipalRateLimitIsSharedAcrossRequests(t *testing.T) {
	h := Handler{rateLimits: NewMemoryRateLimitStore()}
	req := modules.RequestContext{CredentialID: "jwt:principal-a", UserID: "user-a", RateLimitRPM: 1}
	if allowed, _, err := h.checkRateLimit(context.Background(), req, 1); !allowed || err != nil {
		t.Fatalf("first request rejected: %v", err)
	}
	if allowed, _, err := h.checkRateLimit(context.Background(), req, 1); allowed || err != nil {
		t.Fatalf("second request bypassed stable principal limit: %v", err)
	}
	req.CredentialID, req.UserID = "jwt:principal-b", "user-b"
	if allowed, _, err := h.checkRateLimit(context.Background(), req, 1); !allowed || err != nil {
		t.Fatalf("other user shared the first user's quota: %v", err)
	}
}

func TestJWTEmptyGrantsCannotBypassAdmissionThroughOtherAPIs(t *testing.T) {
	h := Handler{}
	req := modules.RequestContext{ModelAccessRestricted: true, ToolAccessRestricted: true}
	if h.authorizeBatchModel(httptest.NewRecorder(), req, "model-a") {
		t.Fatal("batch model bypassed explicit model denial")
	}
	if h.authorizeA2ATaskModel(httptest.NewRecorder(), json.RawMessage(`1`), req, "model-a") {
		t.Fatal("A2A task bypassed explicit model denial")
	}
	connector := "mcp:server@https://mcp.example"
	server := MCPServer{ID: "server", ServerURL: "https://mcp.example"}
	if h.authorizeMCPConnector(httptest.NewRecorder(), req, connector) || h.authorizeMCPTool(httptest.NewRecorder(), req, server, connector, "read") || h.mcpToolAllowed(req, server, connector, "read") {
		t.Fatal("MCP runtime bypassed explicit tool denial")
	}
	req.AllowedModels, req.AllowedTools = []string{"model-a"}, []string{connector}
	if !h.authorizeBatchModel(httptest.NewRecorder(), req, "model-a") || !h.authorizeA2ATaskModel(httptest.NewRecorder(), json.RawMessage(`1`), req, "model-a") || !h.authorizeMCPConnector(httptest.NewRecorder(), req, connector) || !h.mcpToolAllowed(req, server, connector, "read") {
		t.Fatal("explicit grants could not admit supported APIs")
	}
}
