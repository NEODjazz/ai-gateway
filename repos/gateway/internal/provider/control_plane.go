package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"ai-gateway-gateway/internal/modelcatalog"
)

// ControlPlaneSnapshot is the durable management state. Credential material is
// already encrypted by the Router before it crosses the store boundary.
type ControlPlaneSnapshot struct {
	SchemaVersion int                           `json:"schema_version,omitempty"`
	Revision      int64                         `json:"revision"`
	Providers     []ManagedProvider             `json:"providers"`
	Credentials   []EncryptedCredentialSnapshot `json:"credentials"`
	Deployments   []ModelDeployment             `json:"deployments"`
	ModelGroups   []ModelGroup                  `json:"model_groups"`
	Guardrails    []GuardrailPolicy             `json:"guardrails,omitempty"`
	AdminState    json.RawMessage               `json:"admin_state,omitempty"`
	ModelCatalog  json.RawMessage               `json:"model_catalog,omitempty"`
}

type EncryptedCredentialSnapshot struct {
	Credential Credential `json:"credential"`
	Nonce      []byte     `json:"nonce"`
	Ciphertext []byte     `json:"ciphertext"`
}

type adminStateRegistry struct {
	current atomic.Pointer[json.RawMessage]
}

type ControlPlaneStore interface {
	Load(context.Context) (ControlPlaneSnapshot, bool, error)
	Save(context.Context, int64, ControlPlaneSnapshot) (int64, error)
	Revision(context.Context) (int64, error)
}

var ErrControlPlaneConflict = errors.New("control plane revision conflict")

const DefaultControlPlaneRefreshInterval = time.Second

type controlPlaneRuntime struct {
	mu              sync.Mutex
	store           ControlPlaneStore
	revision        int64
	refreshInterval time.Duration
	nextRefresh     atomic.Int64
}

func (r *Router) controlPlaneSnapshot() ControlPlaneSnapshot {
	snapshot := ControlPlaneSnapshot{SchemaVersion: 3}
	if current := r.providers.current.Load(); current != nil {
		for _, item := range *current {
			snapshot.Providers = append(snapshot.Providers, item)
		}
	}
	r.credentials.mu.RLock()
	for _, item := range r.credentials.current {
		snapshot.Credentials = append(snapshot.Credentials, EncryptedCredentialSnapshot{Credential: item.Credential, Nonce: append([]byte(nil), item.Nonce...), Ciphertext: append([]byte(nil), item.Ciphertext...)})
	}
	r.credentials.mu.RUnlock()
	if current := r.deployments.current.Load(); current != nil {
		for _, item := range *current {
			item.Models = append([]string(nil), item.Models...)
			item.Capabilities = append([]string(nil), item.Capabilities...)
			snapshot.Deployments = append(snapshot.Deployments, item)
		}
	}
	if current := r.modelGroups.current.Load(); current != nil {
		for _, item := range *current {
			item.DeploymentIDs = append([]string(nil), item.DeploymentIDs...)
			item.RetryPolicy = cloneRetryPolicy(item.RetryPolicy)
			item.Fallbacks = cloneFallbacks(item.Fallbacks)
			snapshot.ModelGroups = append(snapshot.ModelGroups, item)
		}
	}
	if current := r.guardrails.current.Load(); current != nil {
		for _, item := range *current {
			snapshot.Guardrails = append(snapshot.Guardrails, item)
		}
	}
	if current := r.adminState.current.Load(); current != nil {
		snapshot.AdminState = append(json.RawMessage(nil), (*current)...)
	}
	if r.catalog != nil {
		if payload, err := json.Marshal(r.catalog.Current(context.Background())); err == nil {
			snapshot.ModelCatalog = payload
		}
	}
	sort.Slice(snapshot.Providers, func(i, j int) bool { return snapshot.Providers[i].ID < snapshot.Providers[j].ID })
	sort.Slice(snapshot.Credentials, func(i, j int) bool {
		return snapshot.Credentials[i].Credential.ID < snapshot.Credentials[j].Credential.ID
	})
	sort.Slice(snapshot.Deployments, func(i, j int) bool { return snapshot.Deployments[i].ID < snapshot.Deployments[j].ID })
	sort.Slice(snapshot.ModelGroups, func(i, j int) bool { return snapshot.ModelGroups[i].ID < snapshot.ModelGroups[j].ID })
	sort.Slice(snapshot.Guardrails, func(i, j int) bool { return snapshot.Guardrails[i].Name < snapshot.Guardrails[j].Name })
	return snapshot
}

func (r *Router) applyControlPlaneSnapshot(snapshot ControlPlaneSnapshot) error {
	var catalog modelcatalog.Catalog
	if snapshot.SchemaVersion >= 3 {
		parsed, err := modelcatalog.Parse(string(snapshot.ModelCatalog))
		if err != nil {
			return fmt.Errorf("invalid persisted model catalog: %w", err)
		}
		catalog = parsed
	}
	providers := make(map[string]ManagedProvider, len(snapshot.Providers))
	for _, item := range snapshot.Providers {
		normalized, err := normalizeManagedProvider(item)
		if err != nil || providers[normalized.ID].ID != "" {
			return fmt.Errorf("invalid persisted provider %q", item.ID)
		}
		providers[normalized.ID] = normalized
	}
	credentials := make(map[string]encryptedCredential, len(snapshot.Credentials))
	credentialSecrets := make(map[string]string, len(snapshot.Credentials))
	for _, item := range snapshot.Credentials {
		id := item.Credential.ID
		if id == "" || credentials[id].ID != "" || len(item.Nonce) != r.credentials.aead.NonceSize() || len(item.Ciphertext) == 0 {
			return fmt.Errorf("invalid persisted credential %q", id)
		}
		plaintext, err := r.credentials.aead.Open(nil, item.Nonce, item.Ciphertext, []byte(id))
		if err != nil {
			return fmt.Errorf("decrypt persisted credential %q: %w", id, err)
		}
		if provider := providers[item.Credential.ProviderID]; provider.Type == "bedrock" && provider.AuthType == "aws_sigv4" {
			if _, err := parseAWSCredential(string(plaintext)); err != nil {
				return fmt.Errorf("invalid persisted credential %q for aws_sigv4 provider", id)
			}
		}
		credentials[id] = encryptedCredential{Credential: item.Credential, Nonce: append([]byte(nil), item.Nonce...), Ciphertext: append([]byte(nil), item.Ciphertext...)}
		credentialSecrets[id] = string(plaintext)
	}
	deployments := make(map[string]ModelDeployment, len(snapshot.Deployments))
	for _, item := range snapshot.Deployments {
		if item.ID == "" || deployments[item.ID].ID != "" || providers[item.ProviderID].ID == "" {
			return fmt.Errorf("invalid persisted deployment %q", item.ID)
		}
		if item.CredentialID != "" {
			credential := credentials[item.CredentialID]
			if credential.ID == "" {
				return fmt.Errorf("persisted deployment %q references unknown credential", item.ID)
			}
			if credential.ProviderID != "" && credential.ProviderID != item.ProviderID {
				return fmt.Errorf("persisted deployment %q references credential bound to another provider", item.ID)
			}
		}
		deployments[item.ID] = item
	}
	groups := make(map[string]ModelGroup, len(snapshot.ModelGroups))
	for _, item := range snapshot.ModelGroups {
		if item.ID == "" || groups[item.ID].ID != "" {
			return fmt.Errorf("invalid persisted model group %q", item.ID)
		}
		for _, deploymentID := range item.DeploymentIDs {
			if deployments[deploymentID].ID == "" {
				return fmt.Errorf("persisted model group %q references unknown deployment", item.ID)
			}
		}
		normalized, err := normalizeModelGroupAgainst(item, deployments)
		if err != nil {
			return fmt.Errorf("invalid persisted model group %q", item.ID)
		}
		groups[item.ID] = normalized
	}
	if err := validateModelGroupGraph(groups); err != nil {
		return fmt.Errorf("invalid persisted model group fallback graph: %w", err)
	}
	var guardrails map[string]GuardrailPolicy
	var adminState json.RawMessage
	if snapshot.SchemaVersion >= 2 {
		guardrails = make(map[string]GuardrailPolicy, len(snapshot.Guardrails))
		for _, item := range snapshot.Guardrails {
			normalized, err := normalizeGuardrailPolicy(item.Name, item)
			if err != nil || guardrails[normalized.Name].Name != "" {
				return fmt.Errorf("invalid persisted guardrail policy %q", item.Name)
			}
			guardrails[normalized.Name] = normalized
		}
		adminState = append(json.RawMessage(nil), snapshot.AdminState...)
	}
	previousEndpoints := map[string]Endpoint{}
	for _, endpoint := range r.configuredEndpoints() {
		previousEndpoints[endpoint.Name] = endpoint
	}
	previousProviders := map[string]ManagedProvider{}
	if current := r.providers.current.Load(); current != nil {
		for id, provider := range *current {
			previousProviders[id] = provider
		}
	}
	previousDeployments := map[string]ModelDeployment{}
	if current := r.deployments.current.Load(); current != nil {
		for id, deployment := range *current {
			previousDeployments[id] = deployment
		}
	}
	previousCredentials := map[string]encryptedCredential{}
	r.credentials.mu.RLock()
	for id, credential := range r.credentials.current {
		previousCredentials[id] = credential
	}
	r.credentials.mu.RUnlock()
	endpoints := make([]Endpoint, 0, len(deployments))
	for _, deployment := range deployments {
		managed := providers[deployment.ProviderID]
		existing, endpointFound := previousEndpoints[deployment.ID]
		previousDeployment, deploymentFound := previousDeployments[deployment.ID]
		previousProvider, providerFound := previousProviders[deployment.ProviderID]
		if endpointFound && deploymentFound && providerFound && previousProvider == managed && sameDeploymentRuntime(previousDeployment, deployment) && sameCredentialMaterial(deployment.CredentialID, previousCredentials, credentials) {
			endpoints = append(endpoints, existing)
			continue
		}
		endpoint, err := r.endpointForManagedDeploymentWithSecret(deployment, managed, credentialSecrets[deployment.CredentialID])
		if err != nil {
			return fmt.Errorf("build persisted deployment %q: %w", deployment.ID, err)
		}
		endpoints = append(endpoints, endpoint)
	}
	sort.SliceStable(endpoints, func(i, j int) bool { return endpoints[i].Priority < endpoints[j].Priority })
	r.providers.current.Store(&providers)
	r.credentials.mu.Lock()
	r.credentials.current = credentials
	r.credentials.mu.Unlock()
	r.deployments.current.Store(&deployments)
	r.modelGroups.current.Store(&groups)
	if snapshot.SchemaVersion >= 2 {
		r.guardrails.current.Store(&guardrails)
		r.adminState.current.Store(&adminState)
	}
	if snapshot.SchemaVersion >= 3 && r.catalog != nil {
		r.catalog.SetAuthoritative(catalog)
	}
	r.endpointState.current.Store(&endpoints)
	return nil
}

func sameDeploymentRuntime(first, second ModelDeployment) bool {
	return first.ID == second.ID &&
		first.ProviderID == second.ProviderID &&
		first.CredentialID == second.CredentialID &&
		first.ProviderType == second.ProviderType &&
		first.UpstreamModel == second.UpstreamModel &&
		slices.Equal(first.Models, second.Models) &&
		slices.Equal(first.Capabilities, second.Capabilities) &&
		first.Priority == second.Priority &&
		first.Weight == second.Weight &&
		first.GuardrailPolicy == second.GuardrailPolicy &&
		first.RequestTimeoutMS == second.RequestTimeoutMS &&
		first.MaxRetries == second.MaxRetries &&
		first.CooldownAfterFailures == second.CooldownAfterFailures &&
		first.CooldownSeconds == second.CooldownSeconds &&
		first.MaxParallelRequests == second.MaxParallelRequests &&
		first.QueueCapacity == second.QueueCapacity &&
		first.QueueTimeoutMS == second.QueueTimeoutMS &&
		first.RateLimitRPM == second.RateLimitRPM &&
		first.RateLimitTPM == second.RateLimitTPM &&
		first.Enabled == second.Enabled
}

func sameCredentialMaterial(id string, previous, next map[string]encryptedCredential) bool {
	if id == "" {
		return true
	}
	first, firstFound := previous[id]
	second, secondFound := next[id]
	return firstFound && secondFound && bytes.Equal(first.Nonce, second.Nonce) && bytes.Equal(first.Ciphertext, second.Ciphertext)
}

// AdminState returns the opaque durable state used by gateway management
// registries. Refreshing here makes reads on one replica observe writes made by
// another replica through the existing control-plane revision mechanism.
func (r *Router) AdminState(ctx context.Context) (json.RawMessage, int64, error) {
	if err := r.refreshControlPlane(ctx); err != nil {
		return nil, 0, err
	}
	if r.controlPlane != nil {
		r.controlPlane.mu.Lock()
		defer r.controlPlane.mu.Unlock()
	}
	var payload json.RawMessage
	if current := r.adminState.current.Load(); current != nil {
		payload = append(json.RawMessage(nil), (*current)...)
	}
	revision := int64(0)
	if r.controlPlane != nil {
		revision = r.controlPlane.revision
	}
	return payload, revision, nil
}

// UpdateAdminState atomically persists opaque gateway management state along
// with the provider control plane. A cross-replica conflict is returned to the
// caller and the previous in-memory state is restored.
func (r *Router) UpdateAdminState(ctx context.Context, payload json.RawMessage) (int64, error) {
	previous, unlock, err := r.beginControlMutation(ctx)
	if err != nil {
		return 0, err
	}
	defer unlock()
	next := append(json.RawMessage(nil), payload...)
	r.adminState.current.Store(&next)
	if err := r.persistControlMutation(ctx, previous); err != nil {
		return 0, err
	}
	if r.controlPlane == nil {
		return 0, nil
	}
	return r.controlPlane.revision, nil
}

func (r *Router) beginControlMutation(ctx context.Context) (ControlPlaneSnapshot, func(), error) {
	if r == nil {
		return ControlPlaneSnapshot{}, func() {}, nil
	}
	if r.controlPlane == nil || r.controlPlane.store == nil {
		return r.controlPlaneSnapshot(), func() {}, nil
	}
	r.controlPlane.mu.Lock()
	if err := r.refreshControlPlaneLocked(ctx, true); err != nil {
		r.controlPlane.mu.Unlock()
		return ControlPlaneSnapshot{}, func() {}, err
	}
	return r.controlPlaneSnapshot(), r.controlPlane.mu.Unlock, nil
}

func (r *Router) persistControlMutation(ctx context.Context, previous ControlPlaneSnapshot) error {
	if r == nil || r.controlPlane == nil || r.controlPlane.store == nil {
		return nil
	}
	revision, err := r.controlPlane.store.Save(ctx, r.controlPlane.revision, r.controlPlaneSnapshot())
	if err != nil {
		_ = r.applyControlPlaneSnapshot(previous)
		return err
	}
	r.controlPlane.revision = revision
	r.controlPlane.nextRefresh.Store(time.Now().Add(r.controlPlane.refreshInterval).UnixNano())
	return nil
}

func (r *Router) refreshControlPlane(ctx context.Context) error {
	if r == nil || r.controlPlane == nil || r.controlPlane.store == nil || time.Now().UnixNano() < r.controlPlane.nextRefresh.Load() {
		return nil
	}
	r.controlPlane.mu.Lock()
	defer r.controlPlane.mu.Unlock()
	return r.refreshControlPlaneLocked(ctx, false)
}

func (r *Router) refreshControlPlaneLocked(ctx context.Context, force bool) error {
	if !force && time.Now().UnixNano() < r.controlPlane.nextRefresh.Load() {
		return nil
	}
	r.controlPlane.nextRefresh.Store(time.Now().Add(r.controlPlane.refreshInterval).UnixNano())
	revision, err := r.controlPlane.store.Revision(ctx)
	if err != nil {
		return err
	}
	if revision <= r.controlPlane.revision {
		return nil
	}
	snapshot, found, err := r.controlPlane.store.Load(ctx)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("persisted control plane disappeared")
	}
	if err := r.applyControlPlaneSnapshot(snapshot); err != nil {
		return err
	}
	r.controlPlane.revision = snapshot.Revision
	return nil
}
