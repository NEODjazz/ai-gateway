package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/redisstore"
	"github.com/alicebob/miniredis/v2"
)

func testGlobalLimits(t *testing.T, rpm, tpm int) *GlobalRateLimits {
	t.Helper()
	settings, err := NewGlobalRateLimits(GlobalRateLimitSettings{RPM: rpm, TPM: tpm})
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

func TestGlobalRateLimitsAggregateAcrossCredentialsAndReplicas(t *testing.T) {
	for _, backend := range []string{"memory", "redis"} {
		for _, dimension := range []string{"rpm", "tpm"} {
			t.Run(backend+"/"+dimension, func(t *testing.T) {
				var first, second RateLimitStore
				first = NewMemoryRateLimitStore()
				second = first
				if backend == "redis" {
					addr := miniredis.RunT(t).Addr()
					first = NewRedisRateLimitStore(redisstore.New(redisstore.Config{Addr: addr, Prefix: t.Name()}))
					second = NewRedisRateLimitStore(redisstore.New(redisstore.Config{Addr: addr, Prefix: t.Name()}))
				}
				settings := testGlobalLimits(t, 10, 1000)
				if dimension == "tpm" {
					settings = testGlobalLimits(t, 1000, 10)
				}
				handlers := []Handler{{rateLimits: first, globalRateLimits: settings}, {rateLimits: second, globalRateLimits: settings}}
				var admitted atomic.Int64
				var group sync.WaitGroup
				for i := range 40 {
					group.Add(1)
					go func() {
						defer group.Done()
						req := modules.RequestContext{CredentialID: fmt.Sprintf("key-%d", i), TeamID: fmt.Sprintf("team-%d", i%3)}
						allowed, retry, err := handlers[i%2].checkRateLimit(t.Context(), req, 1)
						if err != nil || (!allowed && retry <= 0) {
							t.Errorf("admission: allowed=%v retry=%v err=%v", allowed, retry, err)
						}
						if allowed {
							admitted.Add(1)
						}
					}()
				}
				group.Wait()
				if admitted.Load() != 10 {
					t.Fatalf("admitted=%d, want 10", admitted.Load())
				}
			})
		}
	}
}

func TestGlobalRateLimitsPreserveCredentialLimitAndWindow(t *testing.T) {
	store := NewMemoryRateLimitStore()
	now := time.Unix(0, 0)
	store.now = func() time.Time { return now }
	settings := testGlobalLimits(t, 2, 10)
	h := Handler{rateLimits: store, globalRateLimits: settings}
	req := modules.RequestContext{CredentialID: "one", RateLimitRPM: 1}
	if allowed, _, err := h.checkRateLimit(t.Context(), req, 5); !allowed || err != nil {
		t.Fatal("first rejected", err)
	}
	if allowed, _, err := h.checkRateLimit(t.Context(), req, 1); allowed || err != nil {
		t.Fatal("credential quota bypassed", err)
	}
	req.CredentialID = "two"
	if allowed, _, err := h.checkRateLimit(t.Context(), req, 5); !allowed || err != nil {
		t.Fatal("denied credential consumed global quota", err)
	}
	settings.apply(&GlobalRateLimitSettings{RPM: 2, TPM: 10})
	req.CredentialID = "three"
	w := httptest.NewRecorder()
	if h.authorizeRateLimit(w, t.Context(), req, 1) || w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal("global quota or retry header missing", w.Code)
	}
	now = now.Add(time.Minute)
	if !h.allowBatchRate(t.Context(), req, 1) {
		t.Fatal("batch did not admit after reset")
	}
	settings.apply(&GlobalRateLimitSettings{})
	if allowed, _, err := h.checkRateLimit(t.Context(), modules.RequestContext{CredentialID: "four"}, 1_000_001); !allowed || err != nil {
		t.Fatal("explicit zero did not disable limits", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	settings.apply(&GlobalRateLimitSettings{RPM: 2})
	if allowed, _, err := h.checkRateLimit(ctx, modules.RequestContext{CredentialID: "five"}, 1); allowed || err == nil {
		t.Fatal("canceled admission succeeded")
	}
}

func TestGlobalRateLimitsSettingsPersistResetAndRollback(t *testing.T) {
	controller := &memoryAdminStateController{}
	settings := testGlobalLimits(t, 60, 100000)
	runtime, err := NewAdminStateRuntime(t.Context(), controller, nil, NewAccessRegistry(), NewMCPRegistry(), NewAgentRegistry(), newLoggingRegistryWithoutWorker(nil), settings)
	if err != nil {
		t.Fatal(err)
	}
	router := Routes(NewHandler(modulesPipeline("admin"), nil).WithGlobalRateLimits(settings).WithAdminState(runtime))
	call := func(method, body string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, "/admin/v1/settings/global-rate-limits", strings.NewReader(body)))
		if w.Code != want {
			t.Fatalf("%s: %d %s", method, w.Code, w.Body.String())
		}
	}
	call("PUT", `{"rpm":5,"tpm":100}`, 200)
	restored := testGlobalLimits(t, 60, 100000)
	replica, err := NewAdminStateRuntime(t.Context(), controller, nil, NewAccessRegistry(), NewMCPRegistry(), NewAgentRegistry(), newLoggingRegistryWithoutWorker(nil), restored)
	if err != nil || restored.effective().RPM != 5 {
		t.Fatal("saved limits not restored", err)
	}
	controller.conflict = true
	call("PUT", `{"rpm":9,"tpm":90}`, 409)
	if settings.effective().RPM != 5 {
		t.Fatal("failed save changed runtime limits")
	}
	controller.conflict = false
	call("DELETE", "", 200)
	if err := replica.Refresh(t.Context()); err != nil || restored.effective().RPM != 60 || restored.snapshot() != nil {
		t.Fatal("reset did not propagate to replica", err)
	}
	for _, body := range []string{`{}`, `{"rpm":1}`, `{"rpm":null,"tpm":1}`, `{"rpm":-1,"tpm":1}`, `{"rpm":1.5,"tpm":2}`, `{"rpm":1,"tpm":1000000001}`, `{"rpm":1,"tpm":2,"secret":"x"}`, `{"rpm":1,"tpm":2} {}`} {
		call("PUT", body, 400)
	}
	for _, role := range []string{"user", "team_admin", "org_admin"} {
		for _, method := range []string{"GET", "PUT", "DELETE"} {
			w := httptest.NewRecorder()
			Routes(NewHandler(modulesPipeline(role), nil).WithGlobalRateLimits(settings)).ServeHTTP(w, httptest.NewRequest(method, "/admin/v1/settings/global-rate-limits", strings.NewReader(`{"rpm":0,"tpm":0}`)))
			if w.Code != 403 {
				t.Fatalf("%s %s: %d", role, method, w.Code)
			}
		}
	}
}

func TestGlobalRateLimitsFailClosedWhenConfigurationIsUnavailable(t *testing.T) {
	controller := &observingAdminStateController{memoryAdminStateController: &memoryAdminStateController{}}
	settings := testGlobalLimits(t, 60, 100000)
	runtime, err := NewAdminStateRuntime(t.Context(), controller, nil, NewAccessRegistry(), NewMCPRegistry(), NewAgentRegistry(), newLoggingRegistryWithoutWorker(nil), settings)
	if err != nil {
		t.Fatal(err)
	}
	controller.failure = errors.New("private storage connection detail")
	store := NewMemoryRateLimitStore()
	h := Handler{rateLimits: store, globalRateLimits: settings, adminState: runtime}
	w := httptest.NewRecorder()
	if h.authorizeRateLimit(w, t.Context(), modules.RequestContext{CredentialID: "one", RateLimitRPM: 1}, 1) || w.Code != 503 {
		t.Fatal("unavailable configuration admitted inference")
	}
	if strings.Contains(w.Body.String(), controller.failure.Error()) || len(store.windows) != 0 {
		t.Fatal("failed configuration exposed storage details or consumed quota")
	}
}
