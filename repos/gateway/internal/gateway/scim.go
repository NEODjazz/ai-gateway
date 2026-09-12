package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ai-gateway-gateway/internal/modules"
)

var scimUserFilterPattern = regexp.MustCompile(`(?i)^\s*(userName|externalId)\s+eq\s+("(?:[^"\\]|\\.)*")\s*$`)

const (
	scimUserSchema      = "urn:ietf:params:scim:schemas:core:2.0:User"
	scimListSchema      = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	scimPatchSchema     = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	scimErrorSchema     = "urn:ietf:params:scim:api:messages:2.0:Error"
	scimResourceBaseURL = "/scim/v2/Users/"
)

type scimValue struct {
	Value string `json:"value"`
	Ref   string `json:"$ref,omitempty"`
}

type scimUserName struct {
	Formatted string `json:"formatted,omitempty"`
}

type scimUser struct {
	Schemas     []string     `json:"schemas"`
	ID          string       `json:"id,omitempty"`
	ExternalID  string       `json:"externalId,omitempty"`
	UserName    string       `json:"userName"`
	Name        scimUserName `json:"name,omitempty"`
	DisplayName string       `json:"displayName,omitempty"`
	Active      *bool        `json:"active,omitempty"`
	Roles       []scimValue  `json:"roles,omitempty"`
	Groups      []scimValue  `json:"groups,omitempty"`
	Meta        *scimMeta    `json:"meta,omitempty"`
}

type scimMeta struct {
	ResourceType string    `json:"resourceType"`
	Created      time.Time `json:"created"`
	LastModified time.Time `json:"lastModified"`
	Location     string    `json:"location"`
}

type scimPatchRequest struct {
	Schemas    []string             `json:"schemas"`
	Operations []scimPatchOperation `json:"Operations"`
}

type scimPatchOperation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

func (h Handler) SCIMServiceProviderConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeSCIM(w, r); !ok {
		return
	}
	writeSCIMJSON(w, http.StatusOK, map[string]any{
		"schemas":               []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
		"patch":                 map[string]bool{"supported": true},
		"bulk":                  map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":                map[string]any{"supported": true, "maxResults": 500},
		"changePassword":        map[string]bool{"supported": false},
		"sort":                  map[string]bool{"supported": false},
		"etag":                  map[string]bool{"supported": false},
		"authenticationSchemes": []map[string]string{{"type": "oauthbearertoken", "name": "Bearer token", "description": "Gateway administrator bearer credential"}},
	})
}

func (h Handler) SCIMResourceTypes(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeSCIM(w, r); !ok {
		return
	}
	resource := map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}, "id": "User", "name": "User", "endpoint": "/Users", "schema": scimUserSchema}
	group := map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}, "id": "Group", "name": "Group", "endpoint": "/Groups", "schema": scimGroupSchema}
	writeSCIMJSON(w, http.StatusOK, scimListResponse([]any{resource, group}, 2, 1, 2))
}

func (h Handler) SCIMSchemas(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeSCIM(w, r); !ok {
		return
	}
	userSchema := map[string]any{
		"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Schema"}, "id": scimUserSchema, "name": "User", "description": "Gateway directory user",
		"attributes": []map[string]any{
			{"name": "userName", "type": "string", "multiValued": false, "required": true, "mutability": "readWrite", "returned": "default", "uniqueness": "server"},
			{"name": "displayName", "type": "string", "multiValued": false, "required": false, "mutability": "readWrite", "returned": "default", "uniqueness": "none"},
			{"name": "active", "type": "boolean", "multiValued": false, "required": false, "mutability": "readWrite", "returned": "default", "uniqueness": "none"},
			{"name": "roles", "type": "complex", "multiValued": true, "required": false, "mutability": "readWrite", "returned": "default", "uniqueness": "none", "subAttributes": []map[string]any{{"name": "value", "type": "string", "multiValued": false, "required": true, "mutability": "readWrite", "returned": "default", "uniqueness": "none"}}},
			{"name": "groups", "type": "complex", "multiValued": true, "required": false, "mutability": "readOnly", "returned": "default", "uniqueness": "none", "subAttributes": []map[string]any{{"name": "value", "type": "string", "multiValued": false, "required": true, "mutability": "readOnly", "returned": "default", "uniqueness": "none"}, {"name": "$ref", "type": "reference", "referenceTypes": []string{"Group"}, "multiValued": false, "required": false, "mutability": "readOnly", "returned": "default", "uniqueness": "none"}}},
		},
	}
	groupSchema := map[string]any{
		"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Schema"}, "id": scimGroupSchema, "name": "Group", "description": "Gateway directory group",
		"attributes": []map[string]any{
			{"name": "displayName", "type": "string", "multiValued": false, "required": true, "mutability": "readWrite", "returned": "default", "uniqueness": "server"},
			{"name": "members", "type": "complex", "multiValued": true, "required": false, "mutability": "readWrite", "returned": "default", "uniqueness": "none", "subAttributes": []map[string]any{{"name": "value", "type": "string", "multiValued": false, "required": true, "mutability": "immutable", "returned": "default", "uniqueness": "none"}, {"name": "$ref", "type": "reference", "referenceTypes": []string{"User"}, "multiValued": false, "required": false, "mutability": "readOnly", "returned": "default", "uniqueness": "none"}}},
		},
	}
	writeSCIMJSON(w, http.StatusOK, scimListResponse([]any{userSchema, groupSchema}, 2, 1, 2))
}

func (h Handler) ListSCIMUsers(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeSCIM(w, r)
	if !ok {
		return
	}
	start, count, ok := scimPagination(w, r)
	if !ok {
		return
	}
	filterAttribute, filterValue, filtered, ok := parseSCIMUserFilter(w, r.URL.Query().Get("filter"))
	if !ok {
		return
	}
	var users []DirectoryUser
	var total int
	var err error
	if filtered {
		var user DirectoryUser
		var found bool
		user, found, err = h.directory.FindUser(r.Context(), managementAudit(req), filterAttribute, filterValue)
		if found {
			total = 1
			if start == 1 && count > 0 {
				users = []DirectoryUser{user}
			}
		}
	} else {
		limit := count
		if limit == 0 {
			limit = 1
		}
		users, total, err = h.directory.ListUsers(r.Context(), managementAudit(req), "", start-1, limit, false)
	}
	if err != nil {
		writeSCIMDirectoryFailure(w, err)
		return
	}
	resources := make([]any, 0, count)
	if count > 0 {
		for _, user := range users {
			resources = append(resources, scimUserFromDirectory(user))
		}
	}
	writeSCIMJSON(w, http.StatusOK, scimListResponse(resources, total, start, len(resources)))
}

func parseSCIMUserFilter(w http.ResponseWriter, raw string) (string, string, bool, bool) {
	if strings.TrimSpace(raw) == "" {
		return "", "", false, true
	}
	match := scimUserFilterPattern.FindStringSubmatch(raw)
	if len(match) != 3 {
		writeSCIMError(w, http.StatusBadRequest, "invalidFilter", "only userName eq and externalId eq filters are supported")
		return "", "", false, false
	}
	value, err := strconv.Unquote(match[2])
	if err != nil || strings.TrimSpace(value) == "" || len(value) > 320 {
		writeSCIMError(w, http.StatusBadRequest, "invalidFilter", "invalid filter value")
		return "", "", false, false
	}
	attribute := "userName"
	if strings.EqualFold(match[1], "externalId") {
		attribute = "externalId"
	}
	return attribute, value, true, true
}

func (h Handler) GetSCIMUser(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeSCIM(w, r)
	if !ok {
		return
	}
	user, err := h.directory.GetUser(r.Context(), managementAudit(req), r.PathValue("id"))
	if err != nil {
		writeSCIMDirectoryFailure(w, err)
		return
	}
	if user.DeletedAt != nil {
		writeSCIMError(w, http.StatusNotFound, "", "user not found")
		return
	}
	writeSCIMJSON(w, http.StatusOK, scimUserFromDirectory(user))
}

func (h Handler) CreateSCIMUser(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeSCIM(w, r)
	if !ok {
		return
	}
	var input scimUser
	if !decodeSCIMJSON(w, r, &input) || !validSCIMUserInput(w, input) {
		return
	}
	id, ok := newSCIMUserID()
	if !ok {
		writeSCIMError(w, http.StatusServiceUnavailable, "", "could not allocate user identifier")
		return
	}
	user := scimUserToDirectory(id, input)
	audit := managementAudit(req)
	event := AuditEvent{Action: "scim.user.create", TargetType: "user", TargetID: id}
	if !h.auditMutation(r.Context(), audit, event) {
		writeSCIMError(w, http.StatusServiceUnavailable, "", "audit service is unavailable")
		return
	}
	saved, err := h.directory.CreateUser(r.Context(), audit, user)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeSCIMDirectoryFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	result := scimUserFromDirectory(saved)
	w.Header().Set("Location", result.Meta.Location)
	writeSCIMJSON(w, http.StatusCreated, result)
}

func (h Handler) ReplaceSCIMUser(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeSCIM(w, r)
	if !ok {
		return
	}
	var input scimUser
	if !decodeSCIMJSON(w, r, &input) || !validSCIMUserInput(w, input) {
		return
	}
	id := r.PathValue("id")
	current, err := h.directory.GetUser(r.Context(), managementAudit(req), id)
	if err != nil {
		writeSCIMDirectoryFailure(w, err)
		return
	}
	if current.DeletedAt != nil {
		writeSCIMError(w, http.StatusNotFound, "", "user not found")
		return
	}
	updated := scimUserToDirectory(id, input)
	updated.TeamIDs = append([]string(nil), current.TeamIDs...)
	h.putSCIMUser(w, r, req, updated, "scim.user.replace")
}

func (h Handler) PatchSCIMUser(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeSCIM(w, r)
	if !ok {
		return
	}
	var patch scimPatchRequest
	if !decodeSCIMJSON(w, r, &patch) {
		return
	}
	if len(patch.Operations) == 0 || len(patch.Operations) > 32 || len(patch.Schemas) != 1 || patch.Schemas[0] != scimPatchSchema {
		writeSCIMError(w, http.StatusBadRequest, "invalidSyntax", "invalid patch request")
		return
	}
	current, err := h.directory.GetUser(r.Context(), managementAudit(req), r.PathValue("id"))
	if err != nil {
		writeSCIMDirectoryFailure(w, err)
		return
	}
	if current.DeletedAt != nil {
		writeSCIMError(w, http.StatusNotFound, "", "user not found")
		return
	}
	input := scimUserFromDirectory(current)
	input.Meta, input.ID = nil, ""
	for _, operation := range patch.Operations {
		if err := applySCIMUserPatch(&input, operation); err != nil {
			writeSCIMError(w, http.StatusBadRequest, "invalidValue", err.Error())
			return
		}
	}
	if !validSCIMUserInput(w, input) {
		return
	}
	updated := scimUserToDirectory(current.ID, input)
	updated.TeamIDs = append([]string(nil), current.TeamIDs...)
	h.putSCIMUser(w, r, req, updated, "scim.user.patch")
}

func (h Handler) DeleteSCIMUser(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeSCIM(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	audit := managementAudit(req)
	event := AuditEvent{Action: "scim.user.delete", TargetType: "user", TargetID: id}
	if !h.auditMutation(r.Context(), audit, event) {
		writeSCIMError(w, http.StatusServiceUnavailable, "", "audit service is unavailable")
		return
	}
	if _, err := h.directory.DeleteUser(r.Context(), audit, id); err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeSCIMDirectoryFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) putSCIMUser(w http.ResponseWriter, r *http.Request, req modules.RequestContext, user DirectoryUser, action string) {
	audit := managementAudit(req)
	event := AuditEvent{Action: action, TargetType: "user", TargetID: user.ID}
	if !h.auditMutation(r.Context(), audit, event) {
		writeSCIMError(w, http.StatusServiceUnavailable, "", "audit service is unavailable")
		return
	}
	saved, err := h.directory.PutUser(r.Context(), audit, user.ID, user)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeSCIMDirectoryFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeSCIMJSON(w, http.StatusOK, scimUserFromDirectory(saved))
}

func scimUserFromDirectory(user DirectoryUser) scimUser {
	active := user.Status == "active"
	roles := make([]scimValue, 0, len(user.Roles))
	for _, role := range user.Roles {
		roles = append(roles, scimValue{Value: role})
	}
	groups := make([]scimValue, 0, len(user.TeamIDs))
	for _, teamID := range user.TeamIDs {
		groups = append(groups, scimValue{Value: teamID, Ref: "/scim/v2/Groups/" + url.PathEscape(teamID)})
	}
	return scimUser{Schemas: []string{scimUserSchema}, ID: user.ID, ExternalID: user.ExternalID, UserName: user.Email, Name: scimUserName{Formatted: user.Name}, DisplayName: user.Name, Active: &active, Roles: roles, Groups: groups, Meta: &scimMeta{ResourceType: "User", Created: user.CreatedAt, LastModified: user.UpdatedAt, Location: scimResourceBaseURL + url.PathEscape(user.ID)}}
}

func scimUserToDirectory(id string, user scimUser) DirectoryUser {
	name := strings.TrimSpace(user.DisplayName)
	if name == "" {
		name = strings.TrimSpace(user.Name.Formatted)
	}
	roles := make([]string, 0, len(user.Roles))
	for _, role := range user.Roles {
		roles = append(roles, strings.TrimSpace(role.Value))
	}
	status := "active"
	if user.Active != nil && !*user.Active {
		status = "disabled"
	}
	return DirectoryUser{ID: id, ExternalID: strings.TrimSpace(user.ExternalID), Email: strings.TrimSpace(user.UserName), Name: name, Status: status, Roles: roles}
}

func validSCIMUserInput(w http.ResponseWriter, user scimUser) bool {
	if len(user.Schemas) != 1 || user.Schemas[0] != scimUserSchema || strings.TrimSpace(user.UserName) == "" || len(user.UserName) > 320 || len(user.ExternalID) > 256 || len(user.DisplayName) > 256 || len(user.Name.Formatted) > 256 || len(user.Roles) > 64 {
		writeSCIMError(w, http.StatusBadRequest, "invalidValue", "invalid user resource")
		return false
	}
	for _, role := range user.Roles {
		if strings.TrimSpace(role.Value) == "" || len(role.Value) > 256 {
			writeSCIMError(w, http.StatusBadRequest, "invalidValue", "invalid role value")
			return false
		}
	}
	return true
}

func applySCIMUserPatch(user *scimUser, operation scimPatchOperation) error {
	op := strings.ToLower(strings.TrimSpace(operation.Op))
	path := strings.ToLower(strings.TrimSpace(operation.Path))
	if op != "add" && op != "replace" {
		return fmt.Errorf("operation %q is not supported", operation.Op)
	}
	if path == "" {
		var values struct {
			ExternalID  *string       `json:"externalId"`
			UserName    *string       `json:"userName"`
			DisplayName *string       `json:"displayName"`
			Active      *bool         `json:"active"`
			Roles       *[]scimValue  `json:"roles"`
			Name        *scimUserName `json:"name"`
		}
		if err := decodeSCIMRaw(operation.Value, &values); err != nil {
			return errors.New("patch value must be an object")
		}
		if values.ExternalID == nil && values.UserName == nil && values.DisplayName == nil && values.Active == nil && values.Roles == nil && values.Name == nil {
			return errors.New("patch value has no supported attributes")
		}
		if values.ExternalID != nil {
			user.ExternalID = *values.ExternalID
		}
		if values.UserName != nil {
			user.UserName = *values.UserName
		}
		if values.DisplayName != nil {
			user.DisplayName = *values.DisplayName
		}
		if values.Active != nil {
			user.Active = values.Active
		}
		if values.Roles != nil {
			user.Roles = *values.Roles
		}
		if values.Name != nil {
			user.Name = *values.Name
			user.DisplayName = values.Name.Formatted
		}
		return nil
	}
	switch path {
	case "externalid":
		return json.Unmarshal(operation.Value, &user.ExternalID)
	case "username":
		return json.Unmarshal(operation.Value, &user.UserName)
	case "displayname", "name.formatted":
		return json.Unmarshal(operation.Value, &user.DisplayName)
	case "active":
		return json.Unmarshal(operation.Value, &user.Active)
	case "roles":
		return json.Unmarshal(operation.Value, &user.Roles)
	default:
		return fmt.Errorf("path %q is not supported", operation.Path)
	}
}

func decodeSCIMRaw(value json.RawMessage, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(value)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("value must contain one JSON value")
	}
	return nil
}

func scimPagination(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	start, count := 1, 100
	var err error
	if raw := strings.TrimSpace(r.URL.Query().Get("startIndex")); raw != "" {
		start, err = strconv.Atoi(raw)
		if err != nil || start < 1 {
			writeSCIMError(w, http.StatusBadRequest, "invalidValue", "startIndex must be at least 1")
			return 0, 0, false
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("count")); raw != "" {
		count, err = strconv.Atoi(raw)
		if err != nil || count < 0 || count > 500 {
			writeSCIMError(w, http.StatusBadRequest, "invalidValue", "count must be between 0 and 500")
			return 0, 0, false
		}
	}
	return start, count, true
}

func scimListResponse(resources []any, total, start, items int) map[string]any {
	return map[string]any{"schemas": []string{scimListSchema}, "totalResults": total, "startIndex": start, "itemsPerPage": items, "Resources": resources}
}

func (h Handler) authorizeSCIM(w http.ResponseWriter, r *http.Request) (modules.RequestContext, bool) {
	if h.directory == nil {
		writeSCIMError(w, http.StatusServiceUnavailable, "", "identity directory is not configured")
		return modules.RequestContext{}, false
	}
	req := modules.RequestContext{APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: requestID(r)}
	if err := h.pipeline.Run(r.Context(), &req); err != nil {
		writeSCIMError(w, http.StatusUnauthorized, "", "invalid bearer credential")
		return req, false
	}
	req.APIKey = ""
	if !hasRole(req.Roles, "admin") {
		writeSCIMError(w, http.StatusForbidden, "", "global admin role is required")
		return req, false
	}
	return req, true
}

func decodeSCIMJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeSCIMError(w, http.StatusBadRequest, "invalidSyntax", "invalid JSON resource")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeSCIMError(w, http.StatusBadRequest, "invalidSyntax", "request must contain one JSON resource")
		return false
	}
	return true
}

func writeSCIMDirectoryFailure(w http.ResponseWriter, err error) {
	var managementErr *ManagementError
	if errors.As(err, &managementErr) {
		switch managementErr.Status {
		case http.StatusBadRequest:
			writeSCIMError(w, http.StatusBadRequest, "invalidValue", "invalid user resource")
			return
		case http.StatusNotFound:
			writeSCIMError(w, http.StatusNotFound, "", "user not found")
			return
		case http.StatusConflict:
			writeSCIMError(w, http.StatusConflict, "uniqueness", "user already exists")
			return
		}
	}
	writeSCIMError(w, http.StatusServiceUnavailable, "", "identity directory is unavailable")
}

func writeSCIMError(w http.ResponseWriter, status int, scimType, detail string) {
	body := map[string]any{"schemas": []string{scimErrorSchema}, "status": strconv.Itoa(status), "detail": detail}
	if scimType != "" {
		body["scimType"] = scimType
	}
	writeSCIMJSON(w, status, body)
}

func writeSCIMJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func newSCIMUserID() (string, bool) {
	return newSCIMResourceID("usr_")
}

func newSCIMResourceID(prefix string) (string, bool) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", false
	}
	return prefix + hex.EncodeToString(value[:]), true
}
