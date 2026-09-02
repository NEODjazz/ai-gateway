package gateway

import (
	"context"
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
type AccessGroupPolicy struct {
	AllowedModels []string
	AllowedTools  []string
}
type PolicyAttachment struct {
	ID         string   `json:"id"`
	PolicyName string   `json:"policy_name"`
	Scope      string   `json:"scope"`
	Teams      []string `json:"teams,omitempty"`
	Keys       []string `json:"keys,omitempty"`
	Models     []string `json:"models,omitempty"`
	Tags       []string `json:"tags,omitempty"`
}
type TagDefinition struct {
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	AllowedModels []string `json:"allowed_models,omitempty"`
	Enabled       bool     `json:"enabled"`
}

type PolicyMatchContext struct {
	TeamID          string
	CredentialID    string
	CredentialAlias string
	Model           string
	Tags            []string
}

type AccessRegistry struct {
	mu          sync.RWMutex
	projects    map[string]Project
	groups      map[string]AccessGroup
	attachments map[string]PolicyAttachment
	tags        map[string]TagDefinition
}

var errInvalidAccessEntry = errors.New("invalid access registry entry")
var errAccessEntryInUse = errors.New("access registry entry is in use")
var errAccessResponseWritten = errors.New("access registry response already written")

func NewAccessRegistry() *AccessRegistry {
	return &AccessRegistry{projects: map[string]Project{}, groups: map[string]AccessGroup{}, attachments: map[string]PolicyAttachment{}, tags: map[string]TagDefinition{}}
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
func (r *AccessRegistry) Project(id string) (Project, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, found := r.projects[strings.TrimSpace(id)]
	if !found {
		return Project{}, false
	}
	item.Tags = append([]string(nil), item.Tags...)
	return item, true
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

func (r *AccessRegistry) Group(id string) (AccessGroup, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, found := r.groups[strings.TrimSpace(id)]
	if !found {
		return AccessGroup{}, false
	}
	item.AllowedModels = append([]string(nil), item.AllowedModels...)
	item.AllowedTools = append([]string(nil), item.AllowedTools...)
	item.Tags = append([]string(nil), item.Tags...)
	return item, true
}

// ResolveAccessGroups returns the union of grants from every assigned group.
// The resulting group grant is still intersected with the virtual key's own
// grants by the request policy layer. A missing or disabled assigned group
// fails closed so deleting a policy cannot silently broaden key access.
func (r *AccessRegistry) ResolveAccessGroups(ids []string) (AccessGroupPolicy, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	policy := AccessGroupPolicy{}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return AccessGroupPolicy{}, errInvalidAccessEntry
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		group, found := r.groups[id]
		if !found || !group.Enabled {
			return AccessGroupPolicy{}, errInvalidAccessEntry
		}
		policy.AllowedModels = append(policy.AllowedModels, group.AllowedModels...)
		policy.AllowedTools = append(policy.AllowedTools, group.AllowedTools...)
	}
	policy.AllowedModels = uniqueStrings(policy.AllowedModels)
	policy.AllowedTools = uniqueStrings(policy.AllowedTools)
	return policy, nil
}
func (r *AccessRegistry) PolicyAttachments() []PolicyAttachment {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]PolicyAttachment, 0, len(r.attachments))
	for _, item := range r.attachments {
		result = append(result, clonePolicyAttachment(item))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func (r *AccessRegistry) Tags() []TagDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]TagDefinition, 0, len(r.tags))
	for _, item := range r.tags {
		item.AllowedModels = append([]string(nil), item.AllowedModels...)
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// TagModelAllowed applies every registered tag constraint carried by a
// credential. Unknown tags remain metadata-only for backward compatibility;
// once registered, disabled tags deny access and model grants intersect.
func (r *AccessRegistry) TagModelAllowed(tags []string, model string) (bool, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, name := range tags {
		definition, ok := r.tags[name]
		if !ok {
			continue
		}
		if !definition.Enabled || (len(definition.AllowedModels) != 0 && !modelAllowed(model, definition.AllowedModels)) {
			return false, name
		}
	}
	return true, ""
}

func (r *AccessRegistry) MatchingPolicyAttachments(context PolicyMatchContext) []PolicyAttachment {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]PolicyAttachment, 0)
	for _, item := range r.attachments {
		if policyAttachmentMatches(item, context) {
			result = append(result, clonePolicyAttachment(item))
		}
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
func (r *AccessRegistry) PutPolicyAttachment(id string, item PolicyAttachment) (PolicyAttachment, error) {
	id = strings.TrimSpace(id)
	item.PolicyName = strings.TrimSpace(item.PolicyName)
	item.Scope = strings.TrimSpace(item.Scope)
	if !validMCPID(id) || item.PolicyName == "" || len(item.PolicyName) > 128 || (item.Scope != "*" && item.Scope != "specific" && item.Scope != "") || !validAccessStrings(item.Teams) || !validAccessStrings(item.Keys) || !validAccessStrings(item.Models) || !validAccessStrings(item.Tags) {
		return PolicyAttachment{}, errInvalidAccessEntry
	}
	if item.Scope == "*" {
		if len(item.Teams) != 0 || len(item.Keys) != 0 || len(item.Models) != 0 || len(item.Tags) != 0 {
			return PolicyAttachment{}, errInvalidAccessEntry
		}
	} else if len(item.Teams) == 0 && len(item.Keys) == 0 && len(item.Models) == 0 && len(item.Tags) == 0 {
		return PolicyAttachment{}, errInvalidAccessEntry
	}
	item.ID = id
	item.Teams = uniqueStrings(item.Teams)
	item.Keys = uniqueStrings(item.Keys)
	item.Models = uniqueStrings(item.Models)
	item.Tags = uniqueStrings(item.Tags)
	r.mu.Lock()
	r.attachments[id] = item
	r.mu.Unlock()
	return clonePolicyAttachment(item), nil
}
func (r *AccessRegistry) PutTag(name string, item TagDefinition) (TagDefinition, error) {
	name = strings.TrimSpace(name)
	item.Description = strings.TrimSpace(item.Description)
	if !validTagName(name) || len(item.Description) > 1024 || !validAccessStrings(item.AllowedModels) {
		return TagDefinition{}, errInvalidAccessEntry
	}
	item.Name = name
	item.AllowedModels = uniqueStrings(item.AllowedModels)
	r.mu.Lock()
	r.tags[name] = item
	r.mu.Unlock()
	return item, nil
}
func validTagName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && !strings.ContainsRune("-_./:", char) {
			return false
		}
	}
	return true
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
func (r *AccessRegistry) DeletePolicyAttachment(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.attachments[id]; !ok {
		return errInvalidAccessEntry
	}
	delete(r.attachments, id)
	return nil
}
func (r *AccessRegistry) DeleteTag(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tags[name]; !ok {
		return errInvalidAccessEntry
	}
	delete(r.tags, name)
	return nil
}

func clonePolicyAttachment(item PolicyAttachment) PolicyAttachment {
	item.Teams = append([]string(nil), item.Teams...)
	item.Keys = append([]string(nil), item.Keys...)
	item.Models = append([]string(nil), item.Models...)
	item.Tags = append([]string(nil), item.Tags...)
	return item
}

func policyAttachmentMatches(item PolicyAttachment, context PolicyMatchContext) bool {
	if item.Scope == "*" {
		return true
	}
	if len(item.Teams) != 0 && !matchesPolicyPattern(context.TeamID, item.Teams) {
		return false
	}
	if len(item.Keys) != 0 && !matchesPolicyPattern(context.CredentialID, item.Keys) && !matchesPolicyPattern(context.CredentialAlias, item.Keys) {
		return false
	}
	if len(item.Models) != 0 && !matchesPolicyPattern(context.Model, item.Models) {
		return false
	}
	if len(item.Tags) != 0 {
		for _, tag := range context.Tags {
			if matchesPolicyPattern(tag, item.Tags) {
				return true
			}
		}
		return false
	}
	return true
}

func policyAttachmentMatchDimensions(item PolicyAttachment, context PolicyMatchContext) []string {
	if item.Scope == "*" {
		return []string{"global"}
	}
	dimensions := make([]string, 0, 4)
	if len(item.Teams) != 0 {
		dimensions = append(dimensions, "team")
	}
	if len(item.Keys) != 0 {
		dimension := "key"
		if matchesPolicyPattern(context.CredentialID, item.Keys) {
			dimension = "key_id"
		} else if matchesPolicyPattern(context.CredentialAlias, item.Keys) {
			dimension = "key_alias"
		}
		dimensions = append(dimensions, dimension)
	}
	if len(item.Models) != 0 {
		dimensions = append(dimensions, "model")
	}
	if len(item.Tags) != 0 {
		dimensions = append(dimensions, "tag")
	}
	return dimensions
}

func matchesPolicyPattern(value string, patterns []string) bool {
	for _, pattern := range patterns {
		if pattern == "*" || pattern == value || (strings.HasSuffix(pattern, "*") && strings.HasPrefix(value, strings.TrimSuffix(pattern, "*"))) {
			return true
		}
	}
	return false
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
func (h Handler) GetProject(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	if h.access == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "access registry is unavailable")
		return
	}
	project, found := h.access.Project(r.PathValue("id"))
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "project was not found")
		return
	}
	writeJSON(w, http.StatusOK, project)
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
func (h Handler) GetAccessGroup(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	if h.access == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "access registry is unavailable")
		return
	}
	group, found := h.access.Group(r.PathValue("id"))
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "access group was not found")
		return
	}
	writeJSON(w, http.StatusOK, group)
}
func (h Handler) ListPolicyAttachments(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	if h.access == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "policy attachment registry is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": h.access.PolicyAttachments()})
}
func (h Handler) ListTags(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	if h.access == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "tag registry is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": h.access.Tags()})
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
func (h Handler) PutPolicyAttachment(w http.ResponseWriter, r *http.Request) {
	var input PolicyAttachment
	h.putAccessEntry(w, r, "policy_attachment", func() (any, error) {
		if !decodeAccessJSON(w, r, &input) {
			return nil, errAccessResponseWritten
		}
		controller, ok := h.guardrailController()
		if !ok {
			return nil, errInvalidAccessEntry
		}
		policy, found := controller.GetGuardrailPolicy(input.PolicyName)
		if !found || !policy.Enabled {
			return nil, errInvalidAccessEntry
		}
		return h.access.PutPolicyAttachment(r.PathValue("id"), input)
	})
}
func (h Handler) PutTag(w http.ResponseWriter, r *http.Request) {
	var input TagDefinition
	h.putAccessEntry(w, r, "tag", func() (any, error) {
		if !decodeAccessJSON(w, r, &input) {
			return nil, errAccessResponseWritten
		}
		return h.access.PutTag(r.PathValue("id"), input)
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
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.access == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "access registry is unavailable")
		return
	}
	id := r.PathValue("id")
	if _, found := h.access.Group(id); !found {
		writeError(w, http.StatusNotFound, "not_found", "access group was not found")
		return
	}
	pager, ok := h.management.(interface {
		ListVirtualKeysPage(context.Context, ManagementAudit, VirtualKeyListFilter) (VirtualKeyPage, error)
	})
	if !ok || pager == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "virtual key reference check is unavailable")
		return
	}
	audit := managementAudit(req)
	event := AuditEvent{Action: "access_group.delete", TargetType: "access_group", TargetID: id}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	page, err := pager.ListVirtualKeysPage(r.Context(), audit, VirtualKeyListFilter{Limit: 1, AccessGroupID: id, Status: "non_revoked"})
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeManagementFailure(w, err)
		return
	}
	if page.Total != 0 {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeError(w, http.StatusConflict, "access_group_in_use", "remove the access group from all non-revoked virtual keys before deleting it")
		return
	}
	if err := h.access.DeleteGroup(id); err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeError(w, http.StatusNotFound, "not_found", "access group was not found")
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	w.WriteHeader(http.StatusNoContent)
}
func (h Handler) DeletePolicyAttachment(w http.ResponseWriter, r *http.Request) {
	h.deleteAccessEntry(w, r, "policy_attachment", func(id string) error { return h.access.DeletePolicyAttachment(id) })
}
func (h Handler) DeleteTag(w http.ResponseWriter, r *http.Request) {
	h.deleteAccessEntry(w, r, "tag", func(name string) error { return h.access.DeleteTag(name) })
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
