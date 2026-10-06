package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modelcatalog"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/promptinjection"
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

func TestControlPlaneRestoresPreviousDeploymentSnapshot(t *testing.T) {
	store := &memoryControlPlaneStore{}
	config := Config{CredentialEncryptionKey: []byte("rollback-snapshot-key"), ControlPlaneStore: store, ControlPlaneRefresh: time.Nanosecond}
	runtime, err := NewWithError(config)
	if err != nil {
		t.Fatal(err)
	}
	router := runtime.(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "provider", Type: "openai-compatible", BaseURL: "https://provider.example/v1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	original := ModelDeployment{ID: "deployment", ProviderID: "provider", Models: []string{"public"}, UpstreamModel: "upstream-v1", Capabilities: []string{"chat"}, Weight: 1, Enabled: true}
	if _, err := router.CreateModelDeployment(original); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	previous := cloneControlPlaneSnapshot(store.snapshot)
	store.mu.Unlock()

	changed := original
	changed.UpstreamModel = "upstream-v2"
	changed.Capabilities = []string{"chat", "tools"}
	if _, err := router.UpdateModelDeployment(original.ID, changed); err != nil {
		t.Fatal(err)
	}
	findDeployment := func() (Endpoint, bool) {
		for _, endpoint := range router.configuredEndpoints() {
			if endpoint.Name == original.ID {
				return endpoint, true
			}
		}
		return Endpoint{}, false
	}
	updated, found := findDeployment()
	if !found || updated.ModelAliases["public"] != "upstream-v2" || !slices.Contains(updated.Capabilities, "tools") {
		t.Fatalf("updated deployment was not active: found=%t alias=%q capabilities=%v", found, updated.ModelAliases["public"], updated.Capabilities)
	}
	store.mu.Lock()
	currentRevision := store.snapshot.Revision
	store.mu.Unlock()
	if _, err := store.Save(t.Context(), currentRevision, previous); err != nil {
		t.Fatal(err)
	}
	if _, _, err := router.AdminState(t.Context()); err != nil {
		t.Fatal(err)
	}
	restored, found := findDeployment()
	if !found || restored.ModelAliases["public"] != "upstream-v1" || slices.Contains(restored.Capabilities, "tools") {
		t.Fatalf("previous deployment was not restored: found=%t alias=%q capabilities=%v", found, restored.ModelAliases["public"], restored.Capabilities)
	}
	reloaded, err := NewWithError(config)
	if err != nil {
		t.Fatal(err)
	}
	deployments := reloaded.(*Router).ListModelDeployments(t.Context())
	if len(deployments) != 1 || deployments[0].UpstreamModel != "upstream-v1" || slices.Contains(deployments[0].Capabilities, "tools") {
		t.Fatalf("restored snapshot was not durable: %+v", deployments)
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
	if _, err := first.UpdateGuardrailPolicyDurable(context.Background(), "strict", GuardrailPolicy{DLP: true, Anonymization: "custom", AnonymizationRules: []string{"phone", "email"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := second.AdminState(context.Background()); err != nil {
		t.Fatal(err)
	}
	policy, found := second.GetGuardrailPolicy("strict")
	if !found || !policy.DLP || !policy.Enabled || policy.Anonymization != "custom" || strings.Join(policy.AnonymizationRules, ",") != "email,phone" {
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

func TestModelOnboardingAtomicallyUpdatesExistingDeployment(t *testing.T) {
	store := &memoryControlPlaneStore{}
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		http.Error(w, "unexpected upstream call", http.StatusInternalServerError)
	}))
	t.Cleanup(upstream.Close)
	runtime, err := NewWithError(Config{CredentialEncryptionKey: []byte("stable-key"), ControlPlaneStore: store})
	if err != nil {
		t.Fatal(err)
	}
	router := runtime.(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "ollama", Type: "ollama", BaseURL: upstream.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	before, _ := modelcatalog.Parse(`{"version":"before","models":[{"provider":"ollama","model":"m","capabilities":["chat","embeddings"]}]}`)
	if _, err := router.UpdateModelCatalog(t.Context(), before); err != nil {
		t.Fatal(err)
	}
	deployment := ModelDeployment{ID: "ollama-m", ProviderID: "ollama", Models: []string{"m"}, Capabilities: []string{"chat", "embeddings"}, Weight: 1, Enabled: true}
	if _, err := router.CreateModelDeployment(deployment); err != nil {
		t.Fatal(err)
	}
	after, _ := modelcatalog.Parse(`{"version":"after","models":[{"provider":"ollama","model":"m","capabilities":["embeddings"]}]}`)
	deployment.Capabilities = []string{"embeddings"}
	input := ModelOnboardingInput{Catalog: after, Deployments: []ModelDeployment{deployment}}
	if _, err := router.PlanModelOnboarding(t.Context(), input); !errors.Is(err, ErrDeploymentExists) {
		t.Fatalf("create-only plan accepted an existing deployment: %v", err)
	}
	input.UpdateExistingDeployments = true
	plan, err := router.PlanModelOnboarding(t.Context(), input)
	if err != nil || !slices.Contains(plan.Changes, "update deployment ollama-m") {
		t.Fatalf("update plan=%+v err=%v", plan, err)
	}
	if current := router.catalog.Current(t.Context()); current.Version != "before" {
		t.Fatalf("plan changed catalog: %+v", current)
	}
	duplicate := input
	duplicate.Deployments = append(append([]ModelDeployment(nil), input.Deployments...), deployment)
	if _, err := router.PlanModelOnboarding(t.Context(), duplicate); !errors.Is(err, ErrInvalidModelOnboarding) {
		t.Fatalf("duplicate deployment plan error = %v", err)
	}
	store.mu.Lock()
	store.saveErr = errors.New("database unavailable")
	store.mu.Unlock()
	if _, err := router.ApplyModelOnboarding(t.Context(), plan.Revision, input); err == nil {
		t.Fatal("expected persistence failure")
	}
	if current := router.catalog.Current(t.Context()); current.Version != "before" {
		t.Fatalf("failed apply leaked catalog: %+v", current)
	}
	if deployments := router.ListModelDeployments(t.Context()); len(deployments) != 1 || !slices.Equal(deployments[0].Capabilities, []string{"chat", "embeddings"}) {
		t.Fatalf("failed apply leaked deployment: %+v", deployments)
	}
	store.mu.Lock()
	store.saveErr = nil
	store.mu.Unlock()
	result, err := router.ApplyModelOnboarding(t.Context(), plan.Revision, input)
	if err != nil || result.Revision != plan.Revision+1 {
		t.Fatalf("apply result=%+v err=%v", result, err)
	}
	if current := router.catalog.Current(t.Context()); current.Version != "after" {
		t.Fatalf("catalog was not updated: %+v", current)
	}
	if deployments := router.ListModelDeployments(t.Context()); len(deployments) != 1 || !slices.Equal(deployments[0].Capabilities, []string{"embeddings"}) {
		t.Fatalf("deployment was not replaced: %+v", deployments)
	}
	request := openai.ChatCompletionRequest{Model: "m", Messages: []openai.Message{{Role: "user", Content: "hello"}}}
	if _, err := router.ChatCompletions(t.Context(), modules.RequestContext{Request: request}); err == nil || upstreamCalls.Load() != 0 {
		t.Fatalf("unsupported chat reached upstream: err=%v calls=%d", err, upstreamCalls.Load())
	}
	if _, err := router.ApplyModelOnboarding(t.Context(), plan.Revision, input); !errors.Is(err, ErrControlPlaneConflict) {
		t.Fatalf("stale plan error = %v", err)
	}
	replicaRuntime, err := NewWithError(Config{CredentialEncryptionKey: []byte("stable-key"), ControlPlaneStore: store})
	if err != nil {
		t.Fatal(err)
	}
	replica := replicaRuntime.(*Router)
	if current := replica.catalog.Current(t.Context()); current.Version != "after" {
		t.Fatalf("replica catalog=%+v", current)
	}
	if deployments := replica.ListModelDeployments(t.Context()); len(deployments) != 1 || !slices.Equal(deployments[0].Capabilities, []string{"embeddings"}) {
		t.Fatalf("replica deployments=%+v", deployments)
	}
}

func TestControlPlanePromptInjectionClassifierPolicyDurableSave(t *testing.T) {
	store := &memoryControlPlaneStore{}
	runtime, err := NewWithError(Config{ControlPlaneStore: store, ControlPlaneRefresh: time.Nanosecond, Endpoints: []config.ProviderEndpointConfig{{Name: "judge", Type: "demo", Models: []string{"judge-model"}}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	saved, err := runtime.(*Router).UpdateGuardrailPolicyDurable(ctx, "protect", GuardrailPolicy{Enabled: true, PromptInjection: &promptinjection.Config{LLMAPICheck: true, JudgeDeploymentID: "judge"}})
	if err != nil || saved.PromptInjection == nil || saved.PromptInjection.JudgeDeploymentID != "judge" {
		t.Fatalf("durable classifier policy: %v", err)
	}
	restored, err := NewWithError(Config{ControlPlaneStore: store})
	if err != nil {
		t.Fatal(err)
	}
	policy, found := restored.(*Router).GetGuardrailPolicy("protect")
	if !found || policy.PromptInjection == nil || !policy.PromptInjection.LLMAPICheck {
		t.Fatal("classifier policy lost during restoration")
	}
	store.mu.Lock()
	store.saveErr = errors.New("database unavailable")
	store.mu.Unlock()
	_, err = runtime.(*Router).UpdateGuardrailPolicyDurable(ctx, "protect", injectionPolicy(promptinjection.Config{HeuristicsCheck: true}))
	if err == nil {
		t.Fatal("expected durable save failure")
	}
	previous, _ := runtime.(*Router).GetGuardrailPolicy("protect")
	if previous.PromptInjection == nil || !previous.PromptInjection.LLMAPICheck {
		t.Fatal("failed persistence changed active detector configuration")
	}
}
