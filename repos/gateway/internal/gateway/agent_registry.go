package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type ToolPolicy struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Description      string   `json:"description,omitempty"`
	AllowedTools     []string `json:"allowed_tools"`
	DeniedTools      []string `json:"denied_tools,omitempty"`
	ApprovalRequired []string `json:"approval_required,omitempty"`
	MaxToolCalls     int      `json:"max_tool_calls"`
	Enabled          bool     `json:"enabled"`
}

type AgentGeneration struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	MaxOutputTokens *int     `json:"max_output_tokens,omitempty"`
}

type AgentMCPTool struct {
	ServerID string `json:"server_id"`
	ToolName string `json:"tool_name"`
}

type AgentProfile struct {
	ID                     string           `json:"id"`
	Name                   string           `json:"name"`
	Description            string           `json:"description,omitempty"`
	Model                  string           `json:"model"`
	InstructionsTemplateID string           `json:"instructions_template_id,omitempty"`
	Instructions           string           `json:"-"`
	InstructionsConfigured bool             `json:"instructions_configured"`
	Generation             *AgentGeneration `json:"generation,omitempty"`
	MCPTools               []AgentMCPTool   `json:"mcp_tools,omitempty"`
	ToolPolicyID           string           `json:"tool_policy_id"`
	AllowedTools           []string         `json:"allowed_tools"`
	DeniedTools            []string         `json:"denied_tools,omitempty"`
	ApprovalRequired       []string         `json:"approval_required,omitempty"`
	MaxToolCalls           int              `json:"max_tool_calls"`
	MaxIterations          int              `json:"max_iterations"`
	Tags                   []string         `json:"tags,omitempty"`
	PolicyMaterializedAt   time.Time        `json:"policy_materialized_at"`
	Enabled                bool             `json:"enabled"`
	ExecutionSupported     bool             `json:"execution_supported"`
	ContentStored          bool             `json:"content_stored"`
}

type AgentRegistry struct {
	mu       sync.RWMutex
	policies map[string]ToolPolicy
	profiles map[string]AgentProfile
}

var errInvalidAgentEntry = errors.New("invalid agent registry entry")
var errAgentResponseWritten = errors.New("agent registry response already written")

func NewAgentRegistry() *AgentRegistry {
	return &AgentRegistry{policies: map[string]ToolPolicy{}, profiles: map[string]AgentProfile{}}
}

func (h Handler) WithAgentRegistry(registry *AgentRegistry) Handler { h.agents = registry; return h }

func (r *AgentRegistry) ToolPolicies() []ToolPolicy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]ToolPolicy, 0, len(r.policies))
	for _, item := range r.policies {
		item.AllowedTools = append([]string(nil), item.AllowedTools...)
		item.DeniedTools = append([]string(nil), item.DeniedTools...)
		item.ApprovalRequired = append([]string(nil), item.ApprovalRequired...)
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (r *AgentRegistry) AgentProfiles() []AgentProfile {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]AgentProfile, 0, len(r.profiles))
	for _, item := range r.profiles {
		result = append(result, cloneAgentProfile(item))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (r *AgentRegistry) AgentProfile(id string) (AgentProfile, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, ok := r.profiles[id]
	if !ok {
		return AgentProfile{}, false
	}
	return cloneAgentProfile(item), true
}

func cloneAgentProfile(item AgentProfile) AgentProfile {
	item.MCPTools = append([]AgentMCPTool(nil), item.MCPTools...)
	item.AllowedTools = append([]string(nil), item.AllowedTools...)
	item.DeniedTools = append([]string(nil), item.DeniedTools...)
	item.ApprovalRequired = append([]string(nil), item.ApprovalRequired...)
	item.Tags = append([]string(nil), item.Tags...)
	if item.Generation != nil {
		generation := *item.Generation
		if generation.Temperature != nil {
			value := *generation.Temperature
			generation.Temperature = &value
		}
		if generation.MaxOutputTokens != nil {
			value := *generation.MaxOutputTokens
			generation.MaxOutputTokens = &value
		}
		item.Generation = &generation
	}
	return item
}

func (r *AgentRegistry) PutToolPolicy(id string, item ToolPolicy) (ToolPolicy, error) {
	id = strings.TrimSpace(id)
	item.Name = strings.TrimSpace(item.Name)
	item.Description = strings.TrimSpace(item.Description)
	if !validMCPID(id) || item.Name == "" || len(item.Name) > 256 || len(item.Description) > 1024 || len(item.AllowedTools) == 0 || !validAccessStrings(item.AllowedTools) || !validAccessStrings(item.DeniedTools) || !validAccessStrings(item.ApprovalRequired) || item.MaxToolCalls < 1 || item.MaxToolCalls > 1000 {
		return ToolPolicy{}, errInvalidAgentEntry
	}
	item.ID = id
	item.AllowedTools = uniqueStrings(item.AllowedTools)
	item.DeniedTools = uniqueStrings(item.DeniedTools)
	item.ApprovalRequired = uniqueStrings(item.ApprovalRequired)
	for _, approval := range item.ApprovalRequired {
		if !toolAllowed(approval, item.AllowedTools) {
			return ToolPolicy{}, errInvalidAgentEntry
		}
	}
	r.mu.Lock()
	r.policies[id] = item
	r.mu.Unlock()
	return item, nil
}

func (r *AgentRegistry) PutAgentProfile(id string, item AgentProfile) (AgentProfile, error) {
	id = strings.TrimSpace(id)
	item.Name = strings.TrimSpace(item.Name)
	item.Description = strings.TrimSpace(item.Description)
	item.Model = strings.TrimSpace(item.Model)
	item.ToolPolicyID = strings.TrimSpace(item.ToolPolicyID)
	item.InstructionsTemplateID = strings.TrimSpace(item.InstructionsTemplateID)
	if !validMCPID(id) || item.Name == "" || len(item.Name) > 256 || len(item.Description) > 1024 || item.Model == "" || len(item.Model) > 256 || !validMCPID(item.ToolPolicyID) || len(item.InstructionsTemplateID) > 256 || !validAccessStrings(item.Tags) || item.MaxIterations < 1 || item.MaxIterations > 50 {
		return AgentProfile{}, errInvalidAgentEntry
	}
	if !validAgentMCPTools(item.MCPTools) {
		return AgentProfile{}, errInvalidAgentEntry
	}
	if len(item.Instructions) > 64<<10 || !utf8.ValidString(item.Instructions) || strings.ContainsRune(item.Instructions, '\x00') || item.Instructions != "" && item.InstructionsTemplateID != "" {
		return AgentProfile{}, errInvalidAgentEntry
	}
	if item.Generation != nil {
		if value := item.Generation.Temperature; value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 2) {
			return AgentProfile{}, errInvalidAgentEntry
		}
		if value := item.Generation.MaxOutputTokens; value != nil && (*value < 1 || *value > 1000000) {
			return AgentProfile{}, errInvalidAgentEntry
		}
		if item.Generation.Temperature == nil && item.Generation.MaxOutputTokens == nil {
			item.Generation = nil
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	policy, ok := r.policies[item.ToolPolicyID]
	if !ok || !policy.Enabled {
		return AgentProfile{}, errInvalidAgentEntry
	}
	item.ID = id
	item.AllowedTools = append([]string(nil), policy.AllowedTools...)
	item.DeniedTools = append([]string(nil), policy.DeniedTools...)
	item.ApprovalRequired = append([]string(nil), policy.ApprovalRequired...)
	item.MaxToolCalls = policy.MaxToolCalls
	item.Tags = uniqueStrings(item.Tags)
	item.PolicyMaterializedAt = time.Now().UTC()
	item.ExecutionSupported = item.Enabled && item.InstructionsTemplateID == ""
	item.InstructionsConfigured = item.Instructions != ""
	item.ContentStored = item.InstructionsConfigured
	r.profiles[id] = cloneAgentProfile(item)
	return cloneAgentProfile(item), nil
}

func (r *AgentRegistry) DeleteToolPolicy(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.policies[id]; !ok {
		return errInvalidAgentEntry
	}
	delete(r.policies, id)
	return nil
}
func (r *AgentRegistry) DeleteAgentProfile(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.profiles[id]; !ok {
		return errInvalidAgentEntry
	}
	delete(r.profiles, id)
	return nil
}

func (h Handler) ListToolPolicies(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	if h.agents == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "agent registry is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": h.agents.ToolPolicies()})
}
func (h Handler) ListAgentProfiles(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	if h.agents == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "agent registry is unavailable")
		return
	}
	profiles := h.agents.AgentProfiles()
	executionSupported, contentStored := false, false
	for _, profile := range profiles {
		executionSupported = executionSupported || profile.ExecutionSupported
		contentStored = contentStored || profile.ContentStored
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"data": profiles, "execution_supported": executionSupported, "content_stored": contentStored})
}

func (h Handler) GetAgentProfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	if h.agents == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "agent registry is unavailable")
		return
	}
	profile, ok := h.agents.AgentProfile(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "agent profile was not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"profile": profile, "instructions": profile.Instructions})
}

func (h Handler) PutToolPolicy(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name             string   `json:"name"`
		Description      string   `json:"description,omitempty"`
		AllowedTools     []string `json:"allowed_tools"`
		DeniedTools      []string `json:"denied_tools,omitempty"`
		ApprovalRequired []string `json:"approval_required,omitempty"`
		MaxToolCalls     int      `json:"max_tool_calls"`
		Enabled          bool     `json:"enabled"`
	}
	h.putAgentEntry(w, r, "tool_policy", func() (any, error) {
		if !decodeAgentJSON(w, r, &input) {
			return nil, errAgentResponseWritten
		}
		return h.agents.PutToolPolicy(r.PathValue("id"), ToolPolicy{Name: input.Name, Description: input.Description, AllowedTools: input.AllowedTools, DeniedTools: input.DeniedTools, ApprovalRequired: input.ApprovalRequired, MaxToolCalls: input.MaxToolCalls, Enabled: input.Enabled})
	})
}
func (h Handler) PutAgentProfile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name                   string           `json:"name"`
		Description            string           `json:"description,omitempty"`
		Model                  string           `json:"model"`
		InstructionsTemplateID string           `json:"instructions_template_id,omitempty"`
		Instructions           *string          `json:"instructions,omitempty"`
		Generation             *AgentGeneration `json:"generation,omitempty"`
		MCPTools               *[]AgentMCPTool  `json:"mcp_tools,omitempty"`
		ToolPolicyID           string           `json:"tool_policy_id"`
		MaxIterations          int              `json:"max_iterations"`
		Tags                   []string         `json:"tags,omitempty"`
		Enabled                bool             `json:"enabled"`
	}
	h.putAgentEntry(w, r, "agent_profile", func() (any, error) {
		if !decodeAgentJSON(w, r, &input) {
			return nil, errAgentResponseWritten
		}
		previous, _ := h.agents.AgentProfile(r.PathValue("id"))
		instructions := previous.Instructions
		if input.Instructions != nil {
			instructions = *input.Instructions
		}
		generation := input.Generation
		if generation == nil {
			generation = previous.Generation
		}
		if instructions != "" && (h.adminState == nil || h.adminState.agentAEAD == nil) {
			writeError(w, http.StatusServiceUnavailable, "configuration_encryption_unavailable", "durable agent instructions require CREDENTIAL_ENCRYPTION_KEY")
			return nil, errAgentResponseWritten
		}
		bindings := previous.MCPTools
		if input.MCPTools != nil {
			bindings = *input.MCPTools
		}
		return h.agents.PutAgentProfile(r.PathValue("id"), AgentProfile{Name: input.Name, Description: input.Description, Model: input.Model, InstructionsTemplateID: input.InstructionsTemplateID, Instructions: instructions, Generation: generation, MCPTools: bindings, ToolPolicyID: input.ToolPolicyID, MaxIterations: input.MaxIterations, Tags: input.Tags, Enabled: input.Enabled})
	})
}
func (h Handler) putAgentEntry(w http.ResponseWriter, r *http.Request, targetType string, save func() (any, error)) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.agents == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "agent registry is unavailable")
		return
	}
	event := AuditEvent{Action: targetType + ".update", TargetType: targetType, TargetID: r.PathValue("id")}
	audit := managementAudit(req)
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	saved, err := save()
	if errors.Is(err, errAgentResponseWritten) {
		h.auditOutcome(r.Context(), audit, event, "failed")
		return
	}
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid agent registry entry")
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusOK, saved)
}
func (h Handler) DeleteToolPolicy(w http.ResponseWriter, r *http.Request) {
	h.deleteAgentEntry(w, r, "tool_policy", h.agentsDeletePolicy)
}
func (h Handler) DeleteAgentProfile(w http.ResponseWriter, r *http.Request) {
	h.deleteAgentEntry(w, r, "agent_profile", h.agentsDeleteProfile)
}
func (h Handler) agentsDeletePolicy(id string) error  { return h.agents.DeleteToolPolicy(id) }
func (h Handler) agentsDeleteProfile(id string) error { return h.agents.DeleteAgentProfile(id) }
func (h Handler) deleteAgentEntry(w http.ResponseWriter, r *http.Request, targetType string, remove func(string) error) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.agents == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "agent registry is unavailable")
		return
	}
	event := AuditEvent{Action: targetType + ".delete", TargetType: targetType, TargetID: r.PathValue("id")}
	audit := managementAudit(req)
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	if err := remove(event.TargetID); err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeError(w, http.StatusNotFound, "not_found", "agent registry entry was not found")
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	w.WriteHeader(http.StatusNoContent)
}
func decodeAgentJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid agent registry entry")
		return false
	}
	return true
}
