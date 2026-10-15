package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
	"ai-gateway-gateway/internal/redisstore"
	"github.com/alicebob/miniredis/v2"
)

func TestIdentityPolicyRuntimeDoesNotClaimDurabilityWithoutStateStore(t *testing.T) {
	llm, err := provider.NewWithError(provider.Config{})
	if err != nil {
		t.Fatal(err)
	}
	access := NewAccessRegistry()
	runtime, err := NewAdminStateRuntime(t.Context(), llm.(AdminStateController), nil, access, NewMCPRegistry(), NewAgentRegistry(), newLoggingRegistryWithoutWorker(nil))
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(modulesPipeline("admin"), nil).WithAccessRegistry(access).WithAdminState(runtime).WithIdentityDirectory(&membershipPagesStub{})
	w := httptest.NewRecorder()
	Routes(h).ServeHTTP(w, httptest.NewRequest("GET", "/admin/v1/identity-policies/team/platform", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"durable":false`) {
		t.Fatal("memory-only runtime claimed durable configuration", w.Code, w.Body.String())
	}
}

type failingIdentityPolicyController struct {
	memoryAdminStateController
	unavailable bool
}

func (c *failingIdentityPolicyController) AdminState(ctx context.Context) (json.RawMessage, int64, error) {
	if c.unavailable {
		return nil, 0, errors.New("test storage unavailable")
	}
	return c.memoryAdminStateController.AdminState(ctx)
}

func TestIdentityPoliciesFailClosedBeforeModelDiscoveryAndQuotaAdmission(t *testing.T) {
	c := &failingIdentityPolicyController{}
	runtime, access, _, _, _ := newTestAdminRuntime(t, c)
	h := NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), fallbackResolverProvider{}).WithAccessRegistry(access).WithAdminState(runtime)
	c.unavailable = true
	w := httptest.NewRecorder()
	Routes(h).ServeHTTP(w, httptest.NewRequest("GET", "/v1/models", nil))
	if w.Code != 503 {
		t.Fatal("storage failure exposed a model inventory", w.Code)
	}
	if allowed, _, err := h.checkRateLimit(t.Context(), modules.RequestContext{CredentialID: "key", RateLimitRPM: 1}, 1); allowed || err == nil {
		t.Fatal("storage failure admitted quota")
	}
	if len(h.rateLimits.(*MemoryRateLimitStore).windows) != 0 {
		t.Fatal("storage failure consumed a credential quota")
	}
}

func TestRealtimeIdentityPolicyRevocationBeforeNextResponse(t *testing.T) {
	r := NewAccessRegistry()
	h := NewHandler(modules.NewPipeline(nil), nil).WithAccessRegistry(r)
	identity := modules.RequestContext{CredentialID: "key", UserID: "alice", AllowedModels: []string{"model"}}
	if allowed, _, err := h.checkRealtimeAdmission(t.Context(), identity, "model", 1); !allowed || err != nil {
		t.Fatal("initial response rejected", err)
	}
	setIdentityPolicy(t, r, IdentityInferencePolicy{ScopeType: "user", ScopeID: "alice", ModelAccess: "deny"})
	if allowed, _, err := h.checkRealtimeAdmission(t.Context(), identity, "model", 1); allowed || !errors.Is(err, errRealtimeModelNotAllowed) {
		t.Fatal("open realtime session bypassed revocation", err)
	}
}

func TestIdentityPolicyCapacityPreservesExistingConfiguration(t *testing.T) {
	r := NewAccessRegistry()
	for i := range 10_000 {
		setIdentityPolicy(t, r, IdentityInferencePolicy{ScopeType: "user", ScopeID: fmt.Sprint(i), ModelAccess: "inherit"})
	}
	if err := r.PutIdentityPolicy(IdentityInferencePolicy{ScopeType: "user", ScopeID: "overflow", ModelAccess: "inherit"}); err == nil {
		t.Fatal("identity policy cardinality is unbounded")
	}
	setIdentityPolicy(t, r, IdentityInferencePolicy{ScopeType: "user", ScopeID: "0", ModelAccess: "deny"})
	if p, ok := r.IdentityPolicy("user", "0"); !ok || p.ModelAccess != "deny" || len(r.IdentityPolicies()) != 10_000 {
		t.Fatal("full registry changed existing configuration")
	}
}

func setIdentityPolicy(t *testing.T, r *AccessRegistry, p IdentityInferencePolicy) {
	t.Helper()
	if err := r.PutIdentityPolicy(p); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityPolicyValidationAndImmutableRegistry(t *testing.T) {
	valid := IdentityInferencePolicy{ScopeType: "team", ScopeID: "platform", ModelAccess: "allowlist", AllowedModels: []string{"qwen*", "exact"}, RPM: 60, TPM: 100000}
	for _, change := range []func(*IdentityInferencePolicy){
		func(p *IdentityInferencePolicy) { p.ScopeType = "organization" }, func(p *IdentityInferencePolicy) { p.ScopeID = "" }, func(p *IdentityInferencePolicy) { p.ModelAccess = "" }, func(p *IdentityInferencePolicy) { p.AllowedModels = nil }, func(p *IdentityInferencePolicy) { p.ModelAccess = "inherit" }, func(p *IdentityInferencePolicy) { p.AllowedModels = []string{"qw*en"} }, func(p *IdentityInferencePolicy) { p.AllowedModels = []string{"qwen?"} }, func(p *IdentityInferencePolicy) { p.AllowedModels = []string{"qwen\n"} }, func(p *IdentityInferencePolicy) { p.RPM = -1 }, func(p *IdentityInferencePolicy) { p.TPM = 1000000001 },
	} {
		p := valid
		change(&p)
		if p.valid() {
			t.Fatalf("accepted invalid policy %+v", p)
		}
	}
	r := NewAccessRegistry()
	setIdentityPolicy(t, r, valid)
	valid.AllowedModels[0] = "mutated"
	p, _ := r.IdentityPolicy("team", "platform")
	p.AllowedModels[0] = "mutated"
	if p, _ = r.IdentityPolicy("team", "platform"); strings.Contains(strings.Join(p.AllowedModels, ","), "mutated") {
		t.Fatal("registry exposed mutable grants")
	}
}

func TestIdentityPolicyAPIRequiresGlobalMutationAndPinsScope(t *testing.T) {
	r := NewAccessRegistry()
	directory := &membershipPagesStub{}
	audit := &recordingAuditClient{}
	h := NewHandler(modulesPipeline("admin"), nil).WithAccessRegistry(r).WithIdentityDirectory(directory).WithAudit(audit)
	call := func(method, path, body string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		Routes(h).ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
	}
	path := "/admin/v1/identity-policies/team/team-a"
	call("GET", path, "", 200)
	call("PUT", path, `{"model_access":"allowlist","allowed_models":["qwen*"],"rpm":2,"tpm":100}`, 200)
	if p, ok := r.IdentityPolicy("team", "team-a"); !ok || p.RPM != 2 || p.ScopeID != "team-a" {
		t.Fatal("saved wrong policy")
	}
	for _, body := range []string{`{"model_access":"inherit","rpm":0}`, `{"model_access":"allowlist","allowed_models":[],"rpm":0,"tpm":0}`, `{"model_access":"deny","rpm":0,"tpm":0,"scope_id":"foreign"}`, `{"model_access":"inherit","rpm":-1,"tpm":0}`, `{"model_access":"inherit","rpm":1.5,"tpm":0}`} {
		call("PUT", path, body, 400)
	}
	if len(audit.events) != 2 {
		t.Fatal("invalid updates were audited as mutations")
	}
	for _, method := range []string{"PUT", "DELETE"} {
		w := httptest.NewRecorder()
		Routes(NewHandler(teamPipeline(), nil).WithAccessRegistry(r).WithIdentityDirectory(directory)).ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(`{"model_access":"inherit","rpm":0,"tpm":0}`)))
		if w.Code != 403 {
			t.Fatal("team admin changed global inference policy", w.Code)
		}
	}
	for _, tc := range []struct {
		path   string
		status int
	}{{path, 200}, {"/admin/v1/identity-policies/team/foreign", 403}, {"/admin/v1/identity-policies/user/user-a", 403}} {
		w := httptest.NewRecorder()
		Routes(NewHandler(teamPipeline(), nil).WithAccessRegistry(r).WithIdentityDirectory(directory)).ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.status {
			t.Fatal("scoped read escaped", w.Code)
		}
	}
	call("DELETE", path, "", 200)
	if _, ok := r.IdentityPolicy("team", "team-a"); ok {
		t.Fatal("reset retained restriction")
	}
}

func TestIdentityModelsIntersectKeyTeamUserAndFallbackGrants(t *testing.T) {
	r := NewAccessRegistry()
	setIdentityPolicy(t, r, IdentityInferencePolicy{ScopeType: "team", ScopeID: "team", ModelAccess: "allowlist", AllowedModels: []string{"test-*", "team-only"}})
	setIdentityPolicy(t, r, IdentityInferencePolicy{ScopeType: "user", ScopeID: "user", ModelAccess: "allowlist", AllowedModels: []string{"test-model", "user-only"}})
	h := NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), fallbackResolverProvider{targets: []string{"test-model", "team-only", "user-only", "key-only"}}).WithAccessRegistry(r)
	metadata := map[string]string{"policy.identity": "stale"}
	req := modules.RequestContext{UserID: "user", TeamID: "team", AllowedModels: []string{"test-model", "key-only"}, Metadata: metadata}
	if !h.prepareAccessGroups(t.Context(), httptest.NewRecorder(), &req) {
		t.Fatal("policy unavailable")
	}
	if metadata["policy.identity"] != "stale" {
		t.Fatal("request preparation mutated shared context metadata")
	}
	if !strings.Contains(req.Metadata["policy.identity"], "team") || strings.Contains(req.Metadata["policy.identity"], "stale") {
		t.Fatal("cache policy scope is stale")
	}
	for _, model := range []string{"test-model", "team-only", "user-only", "key-only"} {
		if got := h.requestModelAllowed(req, model); got != (model == "test-model") {
			t.Fatal("grants did not intersect", model, got)
		}
	}
	if !h.prepareModelFallbacks(httptest.NewRecorder(), t.Context(), &req, "test-model") || len(req.AllowedFallbackModels) != 1 || req.AllowedFallbackModels[0] != "test-model" {
		t.Fatal("fallback escaped identity grants")
	}
	setIdentityPolicy(t, r, IdentityInferencePolicy{ScopeType: "user", ScopeID: "user", ModelAccess: "deny"})
	w := httptest.NewRecorder()
	Routes(h).ServeHTTP(w, httptest.NewRequest("GET", "/v1/models", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"data":[]`) {
		t.Fatal("model discovery exposed blocked models", w.Code, w.Body.String())
	}
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/responses", `{"model":"test-model","input":"hi","store":false}`},
		{"/v1/embeddings", `{"model":"test-model","input":"hi"}`},
	} {
		w := httptest.NewRecorder()
		Routes(h).ServeHTTP(w, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
		if w.Code != 403 {
			t.Fatalf("blocked model reached %s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	r.DeleteIdentityPolicy("team", "team")
	r.DeleteIdentityPolicy("user", "user")
	if !h.prepareAccessGroups(t.Context(), httptest.NewRecorder(), &req) || req.Metadata["policy.identity"] != "" {
		t.Fatal("deleted policy contaminated future cache scope")
	}
}

func TestIdentityQuotasShareCountersAcrossCredentialsAndReplicas(t *testing.T) {
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			r := NewAccessRegistry()
			setIdentityPolicy(t, r, IdentityInferencePolicy{ScopeType: "user", ScopeID: "alice", ModelAccess: "inherit", RPM: 2, TPM: 100})
			setIdentityPolicy(t, r, IdentityInferencePolicy{ScopeType: "team", ScopeID: "team", ModelAccess: "inherit", RPM: 3, TPM: 100})
			first := RateLimitStore(NewMemoryRateLimitStore())
			second := first
			if backend == "redis" {
				server := miniredis.RunT(t)
				first = NewRedisRateLimitStore(redisstore.New(redisstore.Config{Addr: server.Addr(), Prefix: t.Name()}))
				second = NewRedisRateLimitStore(redisstore.New(redisstore.Config{Addr: server.Addr(), Prefix: t.Name()}))
			}
			a := NewHandlerWithRateLimitStore(modules.NewPipeline(nil), nil, first).WithAccessRegistry(r)
			b := NewHandlerWithRateLimitStore(modules.NewPipeline(nil), nil, second).WithAccessRegistry(r)
			steps := []struct {
				h               Handler
				user, key, team string
				tokens          int
				want            bool
			}{{a, "alice", "a", "team", 40, true}, {b, "alice", "b", "team", 40, true}, {a, "alice", "c", "team", 1, false}, {b, "bob", "d", "team", 20, true}, {a, "charlie", "e", "team", 0, false}, {b, "other", "f", "other-team", 100, true}, {a, "outside", "g", "", 100, true}}
			for i, s := range steps {
				allowed, retry, err := s.h.checkRateLimit(t.Context(), modules.RequestContext{CredentialID: s.key, UserID: s.user, TeamID: s.team}, s.tokens)
				if err != nil || allowed != s.want || !allowed && retry <= 0 {
					t.Fatalf("step%d allowed=%v err=%v", i, allowed, err)
				}
			}
			// Independent user quota dimensions are summed across keys, not replaced
			// by taking the minimum of each key's declared limits.
			setIdentityPolicy(t, r, IdentityInferencePolicy{ScopeType: "user", ScopeID: "tokens", ModelAccess: "inherit", TPM: 3})
			for i, want := range []bool{true, true, false} {
				allowed, _, err := a.checkRateLimit(t.Context(), modules.RequestContext{CredentialID: fmt.Sprint(i), UserID: "tokens"}, 1+i%2)
				if err != nil || allowed != want {
					t.Fatal("TPM did not aggregate", i, allowed, err)
				}
			}
		})
	}
}

func TestIdentityQuotaConcurrencyAndWindowTightening(t *testing.T) {
	r := NewAccessRegistry()
	setIdentityPolicy(t, r, IdentityInferencePolicy{ScopeType: "team", ScopeID: "team", ModelAccess: "inherit", RPM: 10})
	store := NewMemoryRateLimitStore()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	h := NewHandlerWithRateLimitStore(modules.NewPipeline(nil), nil, store).WithAccessRegistry(r)
	var count atomic.Int32
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed, _, err := h.checkRateLimit(t.Context(), modules.RequestContext{CredentialID: fmt.Sprint(i), TeamID: "team"}, 1)
			if err != nil {
				t.Error(err)
			}
			if allowed {
				count.Add(1)
			}
		}()
	}
	wg.Wait()
	if count.Load() != 10 {
		t.Fatal("non-atomic team quota", count.Load())
	}
	setIdentityPolicy(t, r, IdentityInferencePolicy{ScopeType: "team", ScopeID: "team", ModelAccess: "inherit", RPM: 5})
	if ok, _, _ := h.checkRateLimit(t.Context(), modules.RequestContext{CredentialID: "fresh", TeamID: "team"}, 0); ok {
		t.Fatal("tightening reset usage")
	}
	now = now.Add(time.Minute)
	if ok, _, _ := h.checkRateLimit(t.Context(), modules.RequestContext{CredentialID: "fresh", TeamID: "team"}, 0); !ok {
		t.Fatal("window never expired")
	}
}

func TestIdentityPolicyDurableRestoreRollbackAndSchema(t *testing.T) {
	c := &memoryAdminStateController{}
	runtime, access, _, _, _ := newTestAdminRuntime(t, c)
	h := NewHandler(modulesPipeline("admin"), nil).WithAccessRegistry(access).WithIdentityDirectory(&membershipPagesStub{}).WithAdminState(runtime)
	put := func(mode string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		Routes(h).ServeHTTP(w, httptest.NewRequest("PUT", "/admin/v1/identity-policies/team/platform", strings.NewReader(`{"model_access":"`+mode+`","rpm":2,"tpm":100}`)))
		if w.Code != want {
			t.Fatalf("save %d: %s", w.Code, w.Body.String())
		}
	}
	put("deny", 200)
	var state adminStateSnapshot
	if json.Unmarshal(c.payload, &state) != nil || state.SchemaVersion != 4 || len(state.IdentityPolicies) != 1 {
		t.Fatal("identity policy version was not persisted")
	}
	_, restored, _, _, _ := newTestAdminRuntime(t, c)
	if p, ok := restored.IdentityPolicy("team", "platform"); !ok || p.ModelAccess != "deny" {
		t.Fatal("restart lost deny")
	}
	c.conflict = true
	put("inherit", 409)
	if p, _ := access.IdentityPolicy("team", "platform"); p.ModelAccess != "deny" {
		t.Fatal("failed save opened access")
	}
	state.SchemaVersion = 3
	payload, _ := json.Marshal(state)
	if runtime.applyPayload(payload, 100) == nil {
		t.Fatal("old schema silently accepted new restrictions")
	}
}
