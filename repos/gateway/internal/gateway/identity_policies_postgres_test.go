package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
	"ai-gateway-gateway/internal/redisstore"
	"github.com/alicebob/miniredis/v2"
)

func TestPostgresIdentityPoliciesRestoreAndReplicaAdmissionIntegration(t *testing.T) {
	store, pool, _ := agentPostgresTestStore(t)
	redis := miniredis.RunT(t)
	fresh := func() Handler {
		t.Helper()
		llm, err := provider.NewWithError(provider.Config{ControlPlaneStore: store, CredentialEncryptionKey: []byte("identity-policy-integration-fixture"), ControlPlaneRefresh: time.Nanosecond})
		if err != nil {
			t.Fatal(err)
		}
		access := NewAccessRegistry()
		runtime, err := NewAdminStateRuntime(t.Context(), llm.(AdminStateController), nil, access, NewMCPRegistry(), NewAgentRegistry(), newLoggingRegistryWithoutWorker(nil))
		if err != nil {
			t.Fatal(err)
		}
		return NewHandlerWithRateLimitStore(modulesPipeline("admin"), nil, NewRedisRateLimitStore(redisstore.New(redisstore.Config{Addr: redis.Addr(), Prefix: t.Name()}))).WithAccessRegistry(access).WithAdminState(runtime).WithIdentityDirectory(&membershipPagesStub{})
	}
	writer, reader := fresh(), fresh()
	call := func(method, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		Routes(writer).ServeHTTP(w, httptest.NewRequest(method, "/admin/v1/identity-policies/team/platform", strings.NewReader(body)))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"durable":true`) {
			t.Fatalf("save %d %s", w.Code, w.Body.String())
		}
	}
	call("PUT", `{"model_access":"allowlist","allowed_models":["qwen*"],"rpm":2,"tpm":100}`)
	var payload string
	if err := pool.QueryRow(t.Context(), `SELECT payload::text FROM gateway_control_plane_state WHERE singleton=TRUE`).Scan(&payload); err != nil || !strings.Contains(payload, "identity_policies") {
		t.Fatal("policy was not persisted", err)
	}
	restored := fresh()
	if p, ok := restored.access.IdentityPolicy("team", "platform"); !ok || p.RPM != 2 || p.ModelAccess != "allowlist" {
		t.Fatal("restart lost restrictions")
	}
	req := modules.RequestContext{UserID: "alice", TeamID: "platform", CredentialID: "one"}
	if !reader.prepareAccessGroups(t.Context(), httptest.NewRecorder(), &req) || !reader.requestModelAllowed(req, "qwen3.8:27b") || reader.requestModelAllowed(req, "foreign-model") {
		t.Fatal("replica model check used stale configuration")
	}
	for i, h := range []Handler{reader, restored, writer} {
		req.CredentialID = string(rune('a' + i))
		allowed, _, err := h.checkRateLimit(t.Context(), req, 40)
		if err != nil || allowed != (i < 2) {
			t.Fatalf("replica %d allowed=%v err=%v", i, allowed, err)
		}
	}
	call("PUT", `{"model_access":"deny","rpm":2,"tpm":100}`)
	if !reader.prepareAccessGroups(t.Context(), httptest.NewRecorder(), &req) || reader.requestModelAllowed(req, "qwen3.8:27b") {
		t.Fatal("model revocation did not reach replica")
	}
	call("DELETE", "")
	if !reader.prepareAccessGroups(t.Context(), httptest.NewRecorder(), &req) || !reader.requestModelAllowed(req, "qwen3.8:27b") || req.Metadata["policy.identity"] != "" {
		t.Fatal("reset did not reach replica")
	}
}
