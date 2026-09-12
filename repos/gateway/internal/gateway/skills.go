package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
	"ai-gateway-gateway/internal/skillstate"
)

type skillIdentity struct {
	ID     string `json:"id"`
	Source struct {
		Type string `json:"type"`
	} `json:"source"`
}

type skillListPayload struct {
	Data    []json.RawMessage `json:"data"`
	HasMore bool              `json:"has_more"`
	FirstID string            `json:"first_id,omitempty"`
	LastID  string            `json:"last_id,omitempty"`
}

func (h Handler) WithSkillStore(store skillstate.Store) Handler { h.skills = store; return h }

func (h Handler) CreateSkill(w http.ResponseWriter, r *http.Request) {
	h.serveSkillRoot(w, r, true)
}

func (h Handler) ListSkills(w http.ResponseWriter, r *http.Request) {
	h.serveSkillRoot(w, r, false)
}

func (h Handler) GetSkill(w http.ResponseWriter, r *http.Request)    { h.serveSkillResource(w, r, "") }
func (h Handler) DeleteSkill(w http.ResponseWriter, r *http.Request) { h.serveSkillResource(w, r, "") }
func (h Handler) UpdateSkill(w http.ResponseWriter, r *http.Request) { h.serveSkillUpdate(w, r) }
func (h Handler) GetSkillContent(w http.ResponseWriter, r *http.Request) {
	h.serveSkillResource(w, r, "content")
}
func (h Handler) CreateSkillVersion(w http.ResponseWriter, r *http.Request) {
	h.serveSkillResource(w, r, "versions")
}
func (h Handler) ListSkillVersions(w http.ResponseWriter, r *http.Request) {
	h.serveSkillResource(w, r, "versions")
}
func (h Handler) GetSkillVersion(w http.ResponseWriter, r *http.Request) {
	h.serveSkillResource(w, r, "versions/"+r.PathValue("version"))
}
func (h Handler) DeleteSkillVersion(w http.ResponseWriter, r *http.Request) {
	h.serveSkillResource(w, r, "versions/"+r.PathValue("version"))
}
func (h Handler) GetSkillVersionContent(w http.ResponseWriter, r *http.Request) {
	h.serveSkillResource(w, r, "versions/"+r.PathValue("version")+"/content")
}

func (h Handler) serveSkillRoot(w http.ResponseWriter, r *http.Request, create bool) {
	req, skills, ok := h.authorizeSkillOperation(w, r)
	if !ok {
		return
	}
	if !validSkillQuery(w, r, !create) {
		return
	}
	transport, ok := decodeSkillTransport(w, r, "skills", create)
	if !ok {
		return
	}
	response, endpoint, err := skills.ExecuteSkillRequest(r.Context(), req, "", transport)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	if create {
		identity, valid := decodeSkillIdentity(response.Body)
		if !valid || identity.Source.Type != "custom" {
			writeError(w, http.StatusBadGateway, "provider_failed", "provider returned an invalid custom skill")
			return
		}
		if _, err := h.skills.ClaimSkill(r.Context(), skillstate.Ownership{SkillID: identity.ID, OwnerKey: skillOwnerKey(req), EndpointID: endpoint}); err != nil {
			_, _, _ = skills.ExecuteSkillRequest(r.Context(), req, endpoint, provider.SkillRequest{Method: http.MethodDelete, Path: "skills/" + identity.ID})
			writeError(w, http.StatusServiceUnavailable, "skill_storage_unavailable", "skill ownership storage is unavailable")
			return
		}
		h.writeSkillResponse(w, response)
		return
	}
	filtered, err := h.filterSkillList(r, req, endpoint, response.Body)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "skill_storage_unavailable", "skill ownership storage is unavailable")
		return
	}
	response.Body = filtered
	h.writeSkillResponse(w, response)
}

func (h Handler) serveSkillResource(w http.ResponseWriter, r *http.Request, suffix string) {
	req, skills, ok := h.authorizeSkillOperation(w, r)
	if !ok {
		return
	}
	if !validSkillQuery(w, r, r.Method == http.MethodGet && suffix == "versions") {
		return
	}
	id := r.PathValue("id")
	if !validSkillID(id) || strings.Contains(suffix, "//") || strings.Contains(suffix, "..") {
		writeError(w, http.StatusBadRequest, "invalid_request", "skill ID or version is invalid")
		return
	}
	if version := r.PathValue("version"); version != "" && !validSkillID(version) {
		writeError(w, http.StatusBadRequest, "invalid_request", "skill ID or version is invalid")
		return
	}
	ownership, err := h.skills.ResolveSkill(r.Context(), skillOwnerKey(req), id)
	if err != nil {
		if !errors.Is(err, skillstate.ErrNotFound) || r.Method != http.MethodGet {
			writeSkillOwnershipError(w, err)
			return
		}
		probe, endpoint, probeErr := skills.ExecuteSkillRequest(r.Context(), req, "", provider.SkillRequest{Method: http.MethodGet, Path: "skills/" + id})
		if probeErr != nil {
			writeProviderFailure(w, probeErr)
			return
		}
		identity, valid := decodeSkillIdentity(probe.Body)
		if !valid || !sharedSkillSource(identity.Source.Type) {
			writeError(w, http.StatusNotFound, "skill_not_found", "skill not found")
			return
		}
		ownership.EndpointID = endpoint
		if suffix == "" {
			h.writeSkillResponse(w, probe)
			return
		}
	}
	path := "skills/" + id
	if suffix != "" {
		path += "/" + suffix
	}
	transport, ok := decodeSkillTransport(w, r, path, r.Method == http.MethodPost)
	if !ok {
		return
	}
	response, _, err := skills.ExecuteSkillRequest(r.Context(), req, ownership.EndpointID, transport)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	if r.Method == http.MethodDelete && suffix == "" {
		if err := h.skills.DeleteSkill(r.Context(), skillOwnerKey(req), id); err != nil {
			writeSkillOwnershipError(w, err)
			return
		}
	}
	h.writeSkillResponse(w, response)
}

type skillUpdateRequest struct {
	DefaultVersion string `json:"default_version"`
}

func (h Handler) serveSkillUpdate(w http.ResponseWriter, r *http.Request) {
	req, skills, ok := h.authorizeSkillOperation(w, r)
	if !ok {
		return
	}
	if !validSkillQuery(w, r, false) {
		return
	}
	id := r.PathValue("id")
	if !validSkillID(id) {
		writeError(w, http.StatusBadRequest, "invalid_request", "skill ID is invalid")
		return
	}
	ownership, err := h.skills.ResolveSkill(r.Context(), skillOwnerKey(req), id)
	if err != nil {
		writeSkillOwnershipError(w, err)
		return
	}
	var update skillUpdateRequest
	if !decodeInferenceRequest(w, r, &update) {
		return
	}
	if !validSkillID(update.DefaultVersion) {
		writeError(w, http.StatusBadRequest, "invalid_request", "default_version is invalid")
		return
	}
	payload, err := json.Marshal(update)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not encode skill update")
		return
	}
	response, _, err := skills.ExecuteSkillRequest(r.Context(), req, ownership.EndpointID, provider.SkillRequest{
		Method:      http.MethodPost,
		Path:        "skills/" + id,
		ContentType: "application/json",
		Body:        payload,
	})
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	identity, valid := decodeSkillIdentity(response.Body)
	if !valid || identity.ID != id || identity.Source.Type != "custom" {
		writeError(w, http.StatusBadGateway, "provider_failed", "provider returned an invalid custom skill")
		return
	}
	h.writeSkillResponse(w, response)
}

func (h Handler) authorizeSkillOperation(w http.ResponseWriter, r *http.Request) (modules.RequestContext, provider.SkillProvider, bool) {
	req := modules.RequestContext{APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: executionID(w), SessionID: sessionID(r), Metadata: map[string]string{"gateway.api_type": "skills"}}
	if err := h.pipeline.RunAuthentication(r.Context(), &req); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
		} else {
			writeError(w, http.StatusBadGateway, "module_failed", "authentication failed")
		}
		return modules.RequestContext{}, nil, false
	}
	req.APIKey = ""
	if h.skills == nil {
		writeError(w, http.StatusServiceUnavailable, "skill_storage_unavailable", "skill ownership storage is unavailable")
		return modules.RequestContext{}, nil, false
	}
	skills, ok := h.provider.(provider.SkillProvider)
	if !ok {
		writeError(w, http.StatusBadGateway, "provider_failed", "Skills are not supported by the configured provider")
		return modules.RequestContext{}, nil, false
	}
	if !h.prepareAccessGroups(w, &req) || !h.authorizeRateLimit(w, r.Context(), req, 0) {
		return modules.RequestContext{}, nil, false
	}
	return req, skills, true
}

func decodeSkillTransport(w http.ResponseWriter, r *http.Request, path string, multipart bool) (provider.SkillRequest, bool) {
	request := provider.SkillRequest{Method: r.Method, Path: path, RawQuery: r.URL.RawQuery, ContentType: r.Header.Get("Content-Type")}
	if !multipart {
		if r.Body != nil && r.ContentLength > 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "request body is not supported")
			return provider.SkillRequest{}, false
		}
		return request, true
	}
	mediaType, parameters, err := mime.ParseMediaType(request.ContentType)
	if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "multipart/form-data is required")
		return provider.SkillRequest{}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, provider.MaxSkillRequestBytes)
	request.Body, err = io.ReadAll(r.Body)
	if err != nil || len(request.Body) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid or oversized skill package")
		return provider.SkillRequest{}, false
	}
	return request, true
}

func validSkillQuery(w http.ResponseWriter, r *http.Request, list bool) bool {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid query parameters")
		return false
	}
	if !list && len(query) != 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return false
	}
	for key, values := range query {
		if (key != "limit" && key != "page" && key != "source") || len(values) != 1 || len(values[0]) > 256 {
			writeError(w, http.StatusBadRequest, "invalid_request", "unsupported or repeated query parameter "+key)
			return false
		}
	}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 1000 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 1000")
			return false
		}
	}
	return true
}

func (h Handler) filterSkillList(r *http.Request, req modules.RequestContext, endpoint string, payload []byte) ([]byte, error) {
	var list skillListPayload
	if json.Unmarshal(payload, &list) != nil || len(list.Data) > 1000 {
		return nil, errors.New("invalid provider skill list")
	}
	ids := make([]string, 0, len(list.Data))
	identities := make([]skillIdentity, len(list.Data))
	for index, raw := range list.Data {
		identity, ok := decodeSkillIdentity(raw)
		if !ok {
			return nil, errors.New("invalid provider skill")
		}
		identities[index] = identity
		if identity.Source.Type == "custom" {
			ids = append(ids, identity.ID)
		}
	}
	owned, err := h.skills.OwnedSkills(r.Context(), skillOwnerKey(req), endpoint, ids)
	if err != nil {
		return nil, err
	}
	filtered := list.Data[:0]
	for index, raw := range list.Data {
		if sharedSkillSource(identities[index].Source.Type) || identities[index].Source.Type == "custom" && owned[identities[index].ID] {
			filtered = append(filtered, raw)
		}
	}
	list.Data = filtered
	list.FirstID, list.LastID = "", ""
	if len(filtered) > 0 {
		list.FirstID = identitiesForRaw(filtered[0]).ID
		list.LastID = identitiesForRaw(filtered[len(filtered)-1]).ID
	}
	return json.Marshal(list)
}

func decodeSkillIdentity(payload []byte) (skillIdentity, bool) {
	var value skillIdentity
	err := json.Unmarshal(payload, &value)
	return value, err == nil && validSkillID(value.ID) && (value.Source.Type == "custom" || value.Source.Type == "anthropic" || value.Source.Type == "anthropic_example" || value.Source.Type == "plugin")
}
func identitiesForRaw(raw json.RawMessage) skillIdentity {
	value, _ := decodeSkillIdentity(raw)
	return value
}
func sharedSkillSource(source string) bool {
	return source == "anthropic" || source == "anthropic_example"
}
func validSkillID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' {
			continue
		}
		return false
	}
	return true
}
func skillOwnerKey(req modules.RequestContext) string { return fileOwnerKey(req) }
func writeSkillOwnershipError(w http.ResponseWriter, err error) {
	if errors.Is(err, skillstate.ErrNotFound) {
		writeError(w, http.StatusNotFound, "skill_not_found", "skill not found")
	} else {
		writeError(w, http.StatusServiceUnavailable, "skill_storage_unavailable", "skill ownership storage is unavailable")
	}
}
func (h Handler) writeSkillResponse(w http.ResponseWriter, response provider.SkillResponse) {
	if response.ContentType != "" {
		w.Header().Set("Content-Type", response.ContentType)
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(response.Body)
}
