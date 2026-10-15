package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
)

func TestPostgresGlobalRateLimitsRestoreAndReplicaRefreshIntegration(t *testing.T) {
	store, _, _ := agentPostgresTestStore(t)
	key := []byte("global-limits-integration-fixture")
	fresh := func() Handler {
		t.Helper()
		llm, err := provider.NewWithError(provider.Config{ControlPlaneStore: store, CredentialEncryptionKey: key, ControlPlaneRefresh: time.Nanosecond})
		if err != nil {
			t.Fatal(err)
		}
		limits := testGlobalLimits(t, 60, 100000)
		runtime, err := NewAdminStateRuntime(t.Context(), llm.(AdminStateController), key, NewAccessRegistry(), NewMCPRegistry(), NewAgentRegistry(), newLoggingRegistryWithoutWorker(nil), limits)
		if err != nil {
			t.Fatal(err)
		}
		return NewHandler(modulesPipeline("admin"), nil).WithGlobalRateLimits(limits).WithAdminState(runtime)
	}
	writer, reader := fresh(), fresh()
	w := httptest.NewRecorder()
	Routes(writer).ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/admin/v1/settings/global-rate-limits", strings.NewReader(`{"rpm":1,"tpm":100}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	restored := fresh()
	if restored.globalRateLimits.effective() != (GlobalRateLimitSettings{RPM: 1, TPM: 100}) {
		t.Fatal("restart lost saved limits")
	}
	for i := range 2 {
		allowed, _, err := reader.checkRateLimit(t.Context(), modules.RequestContext{CredentialID: "reader"}, 1)
		if err != nil || allowed != (i == 0) {
			t.Fatalf("replica admission %d: allowed=%v err=%v", i, allowed, err)
		}
	}
	w = httptest.NewRecorder()
	Routes(writer).ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/admin/v1/settings/global-rate-limits", nil))
	if w.Code != http.StatusOK {
		t.Fatal("reset failed")
	}
	if allowed, _, err := reader.checkRateLimit(t.Context(), modules.RequestContext{CredentialID: "reader"}, 1); !allowed || err != nil {
		t.Fatal("replica retained old quota after reset", err)
	}
	if reader.globalRateLimits.effective().RPM != 60 {
		t.Fatal("replica did not restore ENV default")
	}
}
