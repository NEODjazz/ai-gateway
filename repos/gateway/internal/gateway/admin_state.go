package gateway

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"ai-gateway-gateway/internal/provider"
)

const adminStateSchemaVersion = 1

type AdminStateController interface {
	AdminState(context.Context) (json.RawMessage, int64, error)
	UpdateAdminState(context.Context, json.RawMessage) (int64, error)
}

type encryptedLoggingDestination struct {
	Destination LoggingDestination `json:"destination"`
	Nonce       []byte             `json:"nonce,omitempty"`
	Ciphertext  []byte             `json:"ciphertext,omitempty"`
}

type adminStateSnapshot struct {
	SchemaVersion       int                           `json:"schema_version"`
	Projects            []Project                     `json:"projects,omitempty"`
	AccessGroups        []AccessGroup                 `json:"access_groups,omitempty"`
	MCPServers          []MCPServer                   `json:"mcp_servers,omitempty"`
	MCPToolsets         []MCPToolset                  `json:"mcp_toolsets,omitempty"`
	ToolPolicies        []ToolPolicy                  `json:"tool_policies,omitempty"`
	AgentProfiles       []AgentProfile                `json:"agent_profiles,omitempty"`
	LoggingDestinations []encryptedLoggingDestination `json:"logging_destinations,omitempty"`
}

type AdminStateRuntime struct {
	mu         sync.Mutex
	controller AdminStateController
	revision   int64
	aead       cipher.AEAD
	access     *AccessRegistry
	mcp        *MCPRegistry
	agents     *AgentRegistry
	logging    *LoggingRegistry
}

func NewAdminStateRuntime(ctx context.Context, controller AdminStateController, encryptionKey []byte, access *AccessRegistry, mcp *MCPRegistry, agents *AgentRegistry, logging *LoggingRegistry) (*AdminStateRuntime, error) {
	if controller == nil {
		return nil, errors.New("admin state controller is unavailable")
	}
	key := sha256.Sum256(append([]byte("ai-gateway/admin-state/logging/v1\x00"), encryptionKey...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	runtime := &AdminStateRuntime{controller: controller, aead: aead, access: access, mcp: mcp, agents: agents, logging: logging}
	if err := runtime.refreshLocked(ctx, true); err != nil {
		return nil, err
	}
	return runtime, nil
}

func (r *AdminStateRuntime) Wrap(next http.Handler, mutation bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if err := r.refreshLocked(request.Context(), false); err != nil {
			writeError(w, http.StatusServiceUnavailable, "admin_state_unavailable", "durable admin state is unavailable")
			return
		}
		if !mutation {
			next.ServeHTTP(w, request)
			return
		}
		previous, err := r.snapshot()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "admin_state_failed", "could not capture admin state")
			return
		}
		recorder := httptest.NewRecorder()
		next.ServeHTTP(recorder, request)
		if recorder.Code < 200 || recorder.Code >= 300 {
			copyRecordedResponse(w, recorder)
			return
		}
		payload, err := r.marshal()
		if err == nil {
			r.revision, err = r.controller.UpdateAdminState(request.Context(), payload)
		}
		if err != nil {
			_ = r.apply(previous)
			if errors.Is(err, provider.ErrControlPlaneConflict) {
				writeError(w, http.StatusConflict, "revision_conflict", "control plane changed; refresh and retry")
			} else {
				writeError(w, http.StatusServiceUnavailable, "admin_state_unavailable", "could not persist admin state")
			}
			return
		}
		copyRecordedResponse(w, recorder)
	})
}

func copyRecordedResponse(w http.ResponseWriter, recorder *httptest.ResponseRecorder) {
	for key, values := range recorder.Header() {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(recorder.Code)
	_, _ = io.Copy(w, recorder.Body)
}

func (r *AdminStateRuntime) refreshLocked(ctx context.Context, force bool) error {
	payload, revision, err := r.controller.AdminState(ctx)
	if err != nil {
		return err
	}
	if !force && revision <= r.revision {
		return nil
	}
	if len(payload) != 0 {
		var snapshot adminStateSnapshot
		if err := json.Unmarshal(payload, &snapshot); err != nil {
			return err
		}
		if snapshot.SchemaVersion != adminStateSchemaVersion {
			return errors.New("unsupported admin state schema version")
		}
		if err := r.apply(snapshot); err != nil {
			return err
		}
	}
	r.revision = revision
	return nil
}

func (r *AdminStateRuntime) marshal() (json.RawMessage, error) {
	snapshot, err := r.snapshot()
	if err != nil {
		return nil, err
	}
	return json.Marshal(snapshot)
}

func (r *AdminStateRuntime) snapshot() (adminStateSnapshot, error) {
	snapshot := adminStateSnapshot{SchemaVersion: adminStateSchemaVersion, Projects: r.access.Projects(), AccessGroups: r.access.Groups(), MCPServers: r.mcp.Servers(), MCPToolsets: r.mcp.Toolsets(), ToolPolicies: r.agents.ToolPolicies(), AgentProfiles: r.agents.AgentProfiles()}
	r.logging.mu.RLock()
	defer r.logging.mu.RUnlock()
	for _, entry := range r.logging.destinations {
		item := encryptedLoggingDestination{Destination: entry.destination}
		if entry.secret != "" {
			item.Nonce = make([]byte, r.aead.NonceSize())
			if _, err := rand.Read(item.Nonce); err != nil {
				return adminStateSnapshot{}, err
			}
			item.Ciphertext = r.aead.Seal(nil, item.Nonce, []byte(entry.secret), []byte(entry.destination.ID))
		}
		snapshot.LoggingDestinations = append(snapshot.LoggingDestinations, item)
	}
	return snapshot, nil
}

func (r *AdminStateRuntime) apply(snapshot adminStateSnapshot) error {
	access := NewAccessRegistry()
	for _, item := range snapshot.Projects {
		if _, err := access.PutProject(item.ID, item); err != nil {
			return err
		}
	}
	for _, item := range snapshot.AccessGroups {
		if _, err := access.PutGroup(item.ID, item); err != nil {
			return err
		}
	}
	mcp := NewMCPRegistry()
	for _, item := range snapshot.MCPServers {
		if _, err := mcp.PutServer(item.ID, item); err != nil {
			return err
		}
	}
	for _, item := range snapshot.MCPToolsets {
		if _, err := mcp.PutToolset(item.ID, item); err != nil {
			return err
		}
	}
	agents := NewAgentRegistry()
	for _, item := range snapshot.ToolPolicies {
		if _, err := agents.PutToolPolicy(item.ID, item); err != nil {
			return err
		}
	}
	for _, item := range snapshot.AgentProfiles {
		if _, err := agents.PutAgentProfile(item.ID, item); err != nil {
			return err
		}
		agents.profiles[item.ID] = item
	}
	loggingEntries := make(map[string]loggingDestinationEntry, len(snapshot.LoggingDestinations))
	for _, item := range snapshot.LoggingDestinations {
		secret := ""
		if len(item.Ciphertext) != 0 {
			if len(item.Nonce) != r.aead.NonceSize() {
				return errInvalidLoggingDestination
			}
			plaintext, err := r.aead.Open(nil, item.Nonce, item.Ciphertext, []byte(item.Destination.ID))
			if err != nil {
				return err
			}
			secret = string(plaintext)
		}
		validator := newLoggingRegistryWithoutWorker(r.logging.client)
		validated, err := validator.Put(item.Destination.ID, item.Destination, secret)
		if err != nil {
			return err
		}
		loggingEntries[validated.ID] = loggingDestinationEntry{destination: validated, secret: secret}
	}
	r.access.mu.Lock()
	r.access.projects, r.access.groups = access.projects, access.groups
	r.access.mu.Unlock()
	r.mcp.mu.Lock()
	r.mcp.servers, r.mcp.toolsets = mcp.servers, mcp.toolsets
	r.mcp.mu.Unlock()
	r.agents.mu.Lock()
	r.agents.policies, r.agents.profiles = agents.policies, agents.profiles
	r.agents.mu.Unlock()
	r.logging.mu.Lock()
	r.logging.destinations = loggingEntries
	r.logging.mu.Unlock()
	return nil
}

func isDurableAdminStateRoute(path string) bool {
	prefixes := []string{"/admin/v1/guardrail-policies", "/admin/v1/logging/destinations", "/admin/v1/tool-policies", "/admin/v1/agent-profiles", "/admin/v1/mcp/servers", "/admin/v1/mcp/toolsets", "/admin/v1/projects", "/admin/v1/access-groups"}
	for _, prefix := range prefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func isDurableAdminStateMutation(method, path string) bool {
	if method != http.MethodPut && method != http.MethodDelete {
		return false
	}
	return !strings.HasPrefix(path, "/admin/v1/guardrail-policies") && isDurableAdminStateRoute(path)
}
