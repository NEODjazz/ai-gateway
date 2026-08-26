package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
)

type Project struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	TeamID      string   `json:"team_id,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Enabled     bool     `json:"enabled"`
}
type AccessGroup struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	ProjectID     string   `json:"project_id,omitempty"`
	AllowedModels []string `json:"allowed_models,omitempty"`
	AllowedTools  []string `json:"allowed_tools,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	Enabled       bool     `json:"enabled"`
}
type AccessRegistry struct {
	mu       sync.RWMutex
	projects map[string]Project
	groups   map[string]AccessGroup
}

var errInvalidAccessEntry = errors.New("invalid access registry entry")
var errAccessEntryInUse = errors.New("access registry entry is in use")
var errAccessResponseWritten = errors.New("access registry response already written")

func NewAccessRegistry() *AccessRegistry {
	return &AccessRegistry{projects: map[string]Project{}, groups: map[string]AccessGroup{}}
}
func (h Handler) WithAccessRegistry(registry *AccessRegistry) Handler { h.access = registry; return h }
func (r *AccessRegistry) Projects() []Project {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Project, 0, len(r.projects))
	for _, item := range r.projects {
		item.Tags = append([]string(nil), item.Tags...)
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func (r *AccessRegistry) Groups() []AccessGroup {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]AccessGroup, 0, len(r.groups))
	for _, item := range r.groups {
		item.AllowedModels = append([]string(nil), item.AllowedModels...)
		item.AllowedTools = append([]string(nil), item.AllowedTools...)
		item.Tags = append([]string(nil), item.Tags...)
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func (r *AccessRegistry) PutProject(id string, item Project) (Project, error) {
	id, item.Name, item.Description, item.TeamID = strings.TrimSpace(id), strings.TrimSpace(item.Name), strings.TrimSpace(item.Description), strings.TrimSpace(item.TeamID)
	if !validMCPID(id) || item.Name == "" || len(item.Name) > 256 || len(item.Description) > 1024 || len(item.TeamID) > 256 || !validAccessStrings(item.Tags) {
		return Project{}, errInvalidAccessEntry
	}
	item.ID, item.Tags = id, uniqueStrings(item.Tags)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !item.Enabled {
		for _, group := range r.groups {
			if group.ProjectID == id {
				return Project{}, errAccessEntryInUse
			}
		}
	}
	r.projects[id] = item
	return item, nil
}
func (r *AccessRegistry) PutGroup(id string, item AccessGroup) (AccessGroup, error) {
	id, item.Name, item.Description, item.ProjectID = strings.TrimSpace(id), strings.TrimSpace(item.Name), strings.TrimSpace(item.Description), strings.TrimSpace(item.ProjectID)
	if !validMCPID(id) || item.Name == "" || len(item.Name) > 256 || len(item.Description) > 1024 || !validAccessStrings(item.AllowedModels) || !validAccessStrings(item.AllowedTools) || !validAccessStrings(item.Tags) {
		return AccessGroup{}, errInvalidAccessEntry
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if item.ProjectID != "" {
		project, ok := r.projects[item.ProjectID]
		if !ok || !project.Enabled {
			return AccessGroup{}, errInvalidAccessEntry
		}
	}
	item.ID, item.AllowedModels, item.AllowedTools, item.Tags = id, uniqueStrings(item.AllowedModels), uniqueStrings(item.AllowedTools), uniqueStrings(item.Tags)
	r.groups[id] = item
	return item, nil
}
func (r *AccessRegistry) DeleteProject(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.projects[id]; !ok {
		return errInvalidAccessEntry
	}
	for _, group := range r.groups {
		if group.ProjectID == id {
			return errAccessEntryInUse
		}
	}
	delete(r.projects, id)
	return nil
}
func (r *AccessRegistry) DeleteGroup(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.groups[id]; !ok {
		return errInvalidAccessEntry
	}
	delete(r.groups, id)
	return nil
}
func validAccessStrings(values []string) bool {
	if len(values) > 256 {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > 512 {
			return false
		}
	}
	return true
}

func (h Handler) ListProjects(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	if h.access == nil {
		writeError(w, 503, "management_unavailable", "access registry is unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"data": h.access.Projects()})
}
func (h Handler) ListAccessGroups(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	if h.access == nil {
		writeError(w, 503, "management_unavailable", "access registry is unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"data": h.access.Groups()})
}
func (h Handler) PutProject(w http.ResponseWriter, r *http.Request) {
	var input Project
	h.putAccessEntry(w, r, "project", func() (any, error) {
		if !decodeAccessJSON(w, r, &input) {
			return nil, errAccessResponseWritten
		}
		return h.access.PutProject(r.PathValue("id"), input)
	})
}
func (h Handler) PutAccessGroup(w http.ResponseWriter, r *http.Request) {
	var input AccessGroup
	h.putAccessEntry(w, r, "access_group", func() (any, error) {
		if !decodeAccessJSON(w, r, &input) {
			return nil, errAccessResponseWritten
		}
		return h.access.PutGroup(r.PathValue("id"), input)
	})
}
func (h Handler) putAccessEntry(w http.ResponseWriter, r *http.Request, targetType string, save func() (any, error)) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.access == nil {
		writeError(w, 503, "management_unavailable", "access registry is unavailable")
		return
	}
	event := AuditEvent{Action: targetType + ".update", TargetType: targetType, TargetID: r.PathValue("id")}
	audit := managementAudit(req)
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, 503, "audit_unavailable", "audit service is unavailable")
		return
	}
	saved, err := save()
	if errors.Is(err, errAccessResponseWritten) {
		h.auditOutcome(r.Context(), audit, event, "failed")
		return
	}
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		if errors.Is(err, errAccessEntryInUse) {
			writeError(w, http.StatusConflict, "in_use", "project is referenced by an access group")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid access registry entry")
		}
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, 200, saved)
}
func (h Handler) DeleteProject(w http.ResponseWriter, r *http.Request) {
	h.deleteAccessEntry(w, r, "project", func(id string) error { return h.access.DeleteProject(id) })
}
func (h Handler) DeleteAccessGroup(w http.ResponseWriter, r *http.Request) {
	h.deleteAccessEntry(w, r, "access_group", func(id string) error { return h.access.DeleteGroup(id) })
}
func (h Handler) deleteAccessEntry(w http.ResponseWriter, r *http.Request, targetType string, remove func(string) error) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.access == nil {
		writeError(w, 503, "management_unavailable", "access registry is unavailable")
		return
	}
	event := AuditEvent{Action: targetType + ".delete", TargetType: targetType, TargetID: r.PathValue("id")}
	audit := managementAudit(req)
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, 503, "audit_unavailable", "audit service is unavailable")
		return
	}
	if err := remove(event.TargetID); err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		if errors.Is(err, errAccessEntryInUse) {
			writeError(w, http.StatusConflict, "in_use", "access registry entry is in use")
		} else {
			writeError(w, http.StatusNotFound, "not_found", "access registry entry was not found")
		}
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	w.WriteHeader(204)
}
func decodeAccessJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		writeError(w, 400, "invalid_request", "invalid access registry entry")
		return false
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, 400, "invalid_request", "invalid access registry entry")
		return false
	}
	return true
}
