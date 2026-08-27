package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/config"
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
	if _, err := first.CreateProvider(ManagedProvider{ID: "managed", Type: "demo", Enabled: true}); err != nil {
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
	if secret, err := second.credentialSecret("managed-key"); err != nil || secret != "plaintext-secret" {
		t.Fatalf("credential was not restored: secret=%q err=%v", secret, err)
	}
	if models := second.Models(); len(models) != 2 || models[1].ID != "public" {
		t.Fatalf("persisted models not restored: %+v", models)
	}
	if _, err := first.UpdateProvider("managed", ManagedProvider{Type: "demo", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	providers := second.ListProviders(context.Background())
	if len(providers) != 1 || providers[0].Enabled {
		t.Fatalf("replica did not refresh provider state: %+v", providers)
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
