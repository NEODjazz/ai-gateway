package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modelcatalog"
	"ai-gateway-gateway/internal/openai"
)

type memoryControlPlaneStore struct {
	mu       sync.Mutex
	snapshot ControlPlaneSnapshot
	found    bool
	saveErr  error
}

func (s *memoryControlPlaneStore) Load(context.Context) (ControlPlaneSnapshot, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneControlPlaneSnapshot(s.snapshot), s.found, nil
}

func (s *memoryControlPlaneStore) Save(_ context.Context, expected int64, snapshot ControlPlaneSnapshot) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.snapshot.Revision, s.saveErr
	}
	if s.found && s.snapshot.Revision != expected {
		return s.snapshot.Revision, ErrControlPlaneConflict
	}
	snapshot.Revision = expected + 1
	s.snapshot = cloneControlPlaneSnapshot(snapshot)
	s.found = true
	return snapshot.Revision, nil
}

func (s *memoryControlPlaneStore) Revision(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot.Revision, nil
}

func cloneControlPlaneSnapshot(input ControlPlaneSnapshot) ControlPlaneSnapshot {
	payload, _ := json.Marshal(input)
	var output ControlPlaneSnapshot
	_ = json.Unmarshal(payload, &output)
	return output
}

func TestControlPlanePersistsEncryptedStateAndSynchronizesReplicas(t *testing.T) {
	store := &memoryControlPlaneStore{}
	key := []byte("stable-test-master-key")
	firstProvider, err := NewWithError(Config{CredentialEncryptionKey: key, ControlPlaneStore: store, ControlPlaneRefresh: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	first := firstProvider.(*Router)
	if _, err := first.CreateProvider(ManagedProvider{ID: "managed", Type: "azure-openai", BaseURL: "https://resource.openai.azure.com", APIVersion: "2025-04-01-preview", AuthType: "entra", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.CreateCredential(CredentialInput{ID: "managed-key", ProviderID: "managed", Secret: "plaintext-secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.CreateModelDeployment(ModelDeployment{ID: "managed-deployment", ProviderID: "managed", CredentialID: "managed-key", Models: []string{"internal"}, UpstreamModel: "internal", Weight: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.CreateModelGroup(ModelGroup{ID: "public", DeploymentIDs: []string{"managed-deployment"}, Strategy: "weighted", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	persistedPayload, _ := json.Marshal(store.snapshot)
	store.mu.Unlock()
	if strings.Contains(string(persistedPayload), "plaintext-secret") || !strings.Contains(string(persistedPayload), "ciphertext") {
		t.Fatalf("credential persistence is unsafe: %s", persistedPayload)
	}

	secondProvider, err := NewWithError(Config{CredentialEncryptionKey: key, ControlPlaneStore: store, ControlPlaneRefresh: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	second := secondProvider.(*Router)
	providers := second.ListProviders(context.Background())
	if len(providers) != 1 || providers[0].APIVersion != "2025-04-01-preview" || providers[0].AuthType != "entra" {
		t.Fatalf("Azure provider settings were not restored: %+v", providers)
	}
	if secret, err := second.credentialSecret("managed-key"); err != nil || secret != "plaintext-secret" {
		t.Fatalf("credential was not restored: secret=%q err=%v", secret, err)
	}
	if models := second.Models(); len(models) != 2 || models[1].ID != "public" {
		t.Fatalf("persisted models not restored: %+v", models)
	}
	if _, err := first.UpdateProvider("managed", ManagedProvider{Type: "azure-openai", BaseURL: "https://resource.openai.azure.com", APIVersion: "2025-04-01-preview", AuthType: "entra", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	providers = second.ListProviders(context.Background())
	if len(providers) != 1 || providers[0].Enabled {
		t.Fatalf("replica did not refresh provider state: %+v", providers)
	}
}

func TestControlPlaneRefreshRebuildsOnlyChangedRuntimeEndpoints(t *testing.T) {
	var expectedAuthorization atomic.Value
	expectedAuthorization.Store("Bearer first-secret")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != expectedAuthorization.Load().(string) {
			t.Errorf("path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = fmt.Fprint(w, `{"id":"chatcmpl-control","object":"chat.completion","model":"upstream-v2","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()

	store := &memoryControlPlaneStore{}
	config := Config{CredentialEncryptionKey: []byte("runtime-refresh-stable-key"), ControlPlaneStore: store, ControlPlaneRefresh: time.Nanosecond}
	firstProvider, err := NewWithError(config)
	if err != nil {
		t.Fatal(err)
	}
	first := firstProvider.(*Router)
	if _, err := first.CreateProvider(ManagedProvider{ID: "managed", Type: "openai-compatible", BaseURL: upstream.URL + "/v1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.CreateCredential(CredentialInput{ID: "key", ProviderID: "managed", Secret: "first-secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.CreateModelDeployment(ModelDeployment{ID: "deployment", ProviderID: "managed", CredentialID: "key", UpstreamModel: "upstream-v1", Models: []string{"public-v1"}, Capabilities: []string{"chat"}, Weight: 1, MaxRetries: 1, MaxParallelRequests: 1, RateLimitTPM: 10, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	secondProvider, err := NewWithError(config)
	if err != nil {
		t.Fatal(err)
	}
	second := secondProvider.(*Router)
	before := second.configuredEndpoints()[0]
	if _, err := first.UpdateAdminState(t.Context(), json.RawMessage(`{"revision_only":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := second.AdminState(t.Context()); err != nil {
		t.Fatal(err)
	}
	unchanged := second.configuredEndpoints()[0]
	if unchanged.Admission != before.Admission {
		t.Fatal("unrelated control-plane revision rebuilt runtime endpoint")
	}

	if _, err := first.RotateCredential("key", "second-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.UpdateModelDeployment("deployment", ModelDeployment{ProviderID: "managed", CredentialID: "key", CredentialSet: true, UpstreamModel: "upstream-v2", Models: []string{"public-v2"}, Capabilities: []string{"chat"}, Weight: 2, RequestTimeoutMS: 3000, MaxRetries: 2, MaxParallelRequests: 2, RateLimitTPM: 777, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.UpdateProvider("managed", ManagedProvider{Type: "openai-compatible", BaseURL: upstream.URL + "/v1", RateLimitRPM: 999, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := second.AdminState(t.Context()); err != nil {
		t.Fatal(err)
	}
	changed := second.configuredEndpoints()[0]
	if changed.Admission == before.Admission || changed.MaxRetries != 2 || changed.RequestTimeout != 3*time.Second || changed.RateLimitTPM != 777 || changed.ProviderRateLimitRPM != 999 || len(changed.Models) != 1 || changed.Models[0] != "public-v2" || changed.ModelAliases["public-v2"] != "upstream-v2" {
		t.Fatalf("runtime endpoint was not rebuilt from refreshed state: %+v", changed)
	}
	expectedAuthorization.Store("Bearer second-secret")
	response, err := changed.Provider.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "upstream-v2", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	if err != nil || response.Usage.TotalTokens != 2 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestControlPlaneRejectsIncompatibleSnapshotAtomically(t *testing.T) {
	router := New(Config{CredentialEncryptionKey: []byte("atomic-snapshot-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "managed", Type: "openai-compatible", BaseURL: "https://provider.example/v1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateModelDeployment(ModelDeployment{ID: "deployment", ProviderID: "managed", Models: []string{"public"}, Capabilities: []string{"chat"}, Weight: 1, MaxParallelRequests: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	var beforeEndpoint Endpoint
	for _, endpoint := range router.configuredEndpoints() {
		if endpoint.Name == "deployment" {
			beforeEndpoint = endpoint
		}
	}
	snapshot := router.controlPlaneSnapshot()
	snapshot.Providers[0].Type = "voyage"
	if err := router.applyControlPlaneSnapshot(snapshot); !errors.Is(err, ErrUnsupportedProviderCapability) {
		t.Fatalf("incompatible snapshot error=%v", err)
	}
	providers := router.ListProviders(context.Background())
	deployments := router.ListModelDeployments(context.Background())
	var afterEndpoint Endpoint
	for _, endpoint := range router.configuredEndpoints() {
		if endpoint.Name == "deployment" {
			afterEndpoint = endpoint
		}
	}
	if len(providers) != 1 || providers[0].Type != "openai-compatible" || len(deployments) != 1 || deployments[0].ProviderType != "openai-compatible" || afterEndpoint.Type != "openai-compatible" || afterEndpoint.Admission != beforeEndpoint.Admission {
		t.Fatalf("failed snapshot partially applied: providers=%+v deployments=%+v endpoint=%+v", providers, deployments, afterEndpoint)
	}
}

func TestControlPlaneRollsBackMutationWhenPersistenceFails(t *testing.T) {
	store := &memoryControlPlaneStore{}
	runtime, err := NewWithError(Config{CredentialEncryptionKey: []byte("stable-key"), ControlPlaneStore: store})
	if err != nil {
		t.Fatal(err)
	}
	router := runtime.(*Router)
	store.mu.Lock()
	store.saveErr = errors.New("postgres unavailable")
	store.mu.Unlock()
	if _, err := router.CreateProvider(ManagedProvider{ID: "must-rollback", Type: "demo", Enabled: true}); err == nil {
		t.Fatal("expected persistence failure")
	}
	if providers := router.ListProviders(context.Background()); len(providers) != 0 {
		t.Fatalf("failed mutation leaked into runtime: %+v", providers)
	}
}

func TestControlPlaneRejectsDeploymentCredentialFromAnotherProvider(t *testing.T) {
	store := &memoryControlPlaneStore{}
	key := []byte("stable-provider-boundary-key")
	runtime, err := NewWithError(Config{CredentialEncryptionKey: key, ControlPlaneStore: store})
	if err != nil {
		t.Fatal(err)
	}
	router := runtime.(*Router)
	for _, item := range []ManagedProvider{{ID: "first", Type: "demo", Enabled: true}, {ID: "second", Type: "demo", Enabled: true}} {
		if _, err := router.CreateProvider(item); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "first-key", ProviderID: "first", Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.snapshot.Deployments = append(store.snapshot.Deployments, ModelDeployment{ID: "invalid", ProviderID: "second", CredentialID: "first-key", Models: []string{"model"}, Weight: 1, Enabled: true})
	store.mu.Unlock()
	if _, err := NewWithError(Config{CredentialEncryptionKey: key, ControlPlaneStore: store}); err == nil || !strings.Contains(err.Error(), "credential bound to another provider") {
		t.Fatalf("invalid persisted credential/provider relationship was accepted: %v", err)
	}
}

func TestControlPlaneSynchronizesAdminStateAndGuardrails(t *testing.T) {
	store := &memoryControlPlaneStore{}
	config := Config{CredentialEncryptionKey: []byte("stable-key"), ControlPlaneStore: store, ControlPlaneRefresh: time.Nanosecond}
	firstProvider, err := NewWithError(config)
	if err != nil {
		t.Fatal(err)
	}
	secondProvider, err := NewWithError(config)
	if err != nil {
		t.Fatal(err)
	}
	first, second := firstProvider.(*Router), secondProvider.(*Router)
	payload := json.RawMessage(`{"schema_version":1,"projects":[{"id":"project-a"}]}`)
	if _, err := first.UpdateAdminState(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	restored, _, err := second.AdminState(context.Background())
	if err != nil || string(restored) != string(payload) {
		t.Fatalf("admin state did not synchronize: payload=%s err=%v", restored, err)
	}
	if _, err := first.UpdateGuardrailPolicyDurable(context.Background(), "strict", GuardrailPolicy{DLP: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := second.AdminState(context.Background()); err != nil {
		t.Fatal(err)
	}
	policy, found := second.GetGuardrailPolicy("strict")
	if !found || !policy.DLP || !policy.Enabled {
		t.Fatalf("guardrail did not synchronize: %+v found=%v", policy, found)
	}
}

func TestLegacyControlPlaneSnapshotPreservesConfiguredGuardrails(t *testing.T) {
	store := &memoryControlPlaneStore{found: true, snapshot: ControlPlaneSnapshot{Revision: 1}}
	runtime, err := NewWithError(Config{CredentialEncryptionKey: []byte("stable-key"), ControlPlaneStore: store, GuardrailPolicies: map[string]config.GuardrailPolicyConfig{"legacy": {DLP: true}}})
	if err != nil {
		t.Fatal(err)
	}
	policy, found := runtime.(*Router).GetGuardrailPolicy("legacy")
	if !found || !policy.DLP {
		t.Fatalf("legacy snapshot removed configured guardrail: %+v found=%v", policy, found)
	}
}

func TestLegacyControlPlaneSnapshotImportsRuntimeCatalogOnNextMutation(t *testing.T) {
	legacyCatalog, _ := modelcatalog.Parse(`{"version":"legacy-redis","models":[{"provider":"p","model":"m"}]}`)
	registry := modelcatalog.NewRegistry(legacyCatalog, nil, time.Second)
	store := &memoryControlPlaneStore{found: true, snapshot: ControlPlaneSnapshot{SchemaVersion: 2, Revision: 1}}
	runtime, err := NewWithError(Config{CredentialEncryptionKey: []byte("stable-key"), CatalogRegistry: registry, ControlPlaneStore: store})
	if err != nil {
		t.Fatal(err)
	}
	router := runtime.(*Router)
	if current := router.catalog.Current(context.Background()); current.Version != "legacy-redis" {
		t.Fatalf("legacy catalog was not preserved: %+v", current)
	}
	if _, err := router.CreateProvider(ManagedProvider{ID: "managed", Type: "demo", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	persisted := cloneControlPlaneSnapshot(store.snapshot)
	store.mu.Unlock()
	if persisted.SchemaVersion != 3 {
		t.Fatalf("schema version = %d", persisted.SchemaVersion)
	}
	catalog, err := modelcatalog.Parse(string(persisted.ModelCatalog))
	if err != nil || catalog.Version != "legacy-redis" {
		t.Fatalf("legacy catalog was not migrated: catalog=%+v err=%v", catalog, err)
	}
}

func TestModelOnboardingPlansAndCommitsOneControlPlaneRevision(t *testing.T) {
	store := &memoryControlPlaneStore{}
	runtime, err := NewWithError(Config{CredentialEncryptionKey: []byte("stable-key"), ControlPlaneStore: store})
	if err != nil {
		t.Fatal(err)
	}
	router := runtime.(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "managed", Type: "demo", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	initialCatalog, _ := modelcatalog.Parse(`{"version":"before","models":[]}`)
	if _, err := router.UpdateModelCatalog(context.Background(), initialCatalog); err != nil {
		t.Fatal(err)
	}
	nextCatalog, _ := modelcatalog.Parse(`{"version":"onboard-v1","models":[{"provider":"managed","model":"public","capabilities":["chat"]}]}`)
	input := ModelOnboardingInput{
		Catalog:     nextCatalog,
		Deployments: []ModelDeployment{{ID: "managed-public", ProviderID: "managed", UpstreamModel: "upstream", Models: []string{"public"}, Capabilities: []string{"chat"}, Weight: 1, Enabled: true}},
		ModelGroups: []ModelGroup{{ID: "public", DeploymentIDs: []string{"managed-public"}, Strategy: "weighted", Enabled: true}},
	}
	plan, err := router.PlanModelOnboarding(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	beforeRevision := plan.Revision
	if current := router.catalog.Current(context.Background()); current.Version != "before" {
		t.Fatalf("plan mutated catalog: %+v", current)
	}
	if deployments := router.ListModelDeployments(context.Background()); len(deployments) != 0 {
		t.Fatalf("plan mutated deployments: %+v", deployments)
	}
	result, err := router.ApplyModelOnboarding(context.Background(), plan.Revision, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != beforeRevision+1 {
		t.Fatalf("apply revision = %d, want %d", result.Revision, beforeRevision+1)
	}
	if current := router.catalog.Current(context.Background()); current.Version != "onboard-v1" {
		t.Fatalf("catalog was not committed: %+v", current)
	}
	if groups := router.ListModelGroups(context.Background()); len(groups) != 1 || groups[0].ID != "public" {
		t.Fatalf("model group was not committed: %+v", groups)
	}

	replicaProvider, err := NewWithError(Config{CredentialEncryptionKey: []byte("stable-key"), ControlPlaneStore: store})
	if err != nil {
		t.Fatal(err)
	}
	replica := replicaProvider.(*Router)
	if current := replica.catalog.Current(context.Background()); current.Version != "onboard-v1" {
		t.Fatalf("replica did not restore catalog: %+v", current)
	}
	if models := replica.Models(); len(models) != 1 || models[0].ID != "public" {
		t.Fatalf("replica did not restore routable model: %+v", models)
	}
}

func TestModelOnboardingRejectsStalePlanAndRollsBackFailedPersistence(t *testing.T) {
	store := &memoryControlPlaneStore{}
	runtime, err := NewWithError(Config{CredentialEncryptionKey: []byte("stable-key"), ControlPlaneStore: store})
	if err != nil {
		t.Fatal(err)
	}
	router := runtime.(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "managed", Type: "demo", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateModelDeployment(ModelDeployment{ID: "existing", ProviderID: "managed", Models: []string{"existing"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateModelGroup(ModelGroup{ID: "existing", DeploymentIDs: []string{"existing"}, Strategy: "weighted", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	initialCatalog, _ := modelcatalog.Parse(`{"version":"before","models":[]}`)
	if _, err := router.UpdateModelCatalog(context.Background(), initialCatalog); err != nil {
		t.Fatal(err)
	}
	nextCatalog, _ := modelcatalog.Parse(`{"version":"after","models":[{"provider":"managed","model":"public"}]}`)
	input := ModelOnboardingInput{Catalog: nextCatalog, Deployments: []ModelDeployment{{ID: "deployment", ProviderID: "managed", Models: []string{"public"}, Enabled: true}}}
	plan, err := router.PlanModelOnboarding(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.UpdateProvider("managed", ManagedProvider{Type: "demo", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.ApplyModelOnboarding(context.Background(), plan.Revision, input); !errors.Is(err, ErrControlPlaneConflict) {
		t.Fatalf("stale apply error = %v", err)
	}
	if current := router.catalog.Current(context.Background()); current.Version != "before" {
		t.Fatalf("stale plan changed catalog: %+v", current)
	}

	plan, err = router.PlanModelOnboarding(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.saveErr = errors.New("postgres unavailable")
	store.mu.Unlock()
	if _, err := router.ApplyModelOnboarding(context.Background(), plan.Revision, input); err == nil {
		t.Fatal("expected persistence failure")
	}
	if current := router.catalog.Current(context.Background()); current.Version != "before" {
		t.Fatalf("failed apply leaked catalog: %+v", current)
	}
	if deployments := router.ListModelDeployments(context.Background()); len(deployments) != 1 || deployments[0].ID != "existing" {
		t.Fatalf("failed apply leaked deployments: %+v", deployments)
	}
	if groups := router.ListModelGroups(context.Background()); len(groups) != 1 || groups[0].ID != "existing" {
		t.Fatalf("failed apply corrupted existing groups: %+v", groups)
	}
}
