package gateway

import (
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

	"ai-gateway-gateway/internal/modelcatalog"
	"ai-gateway-gateway/internal/provider"
)

func (h Handler) WithModelRegistry(registry *modelcatalog.Registry) Handler {
	h.models = registry
	return h
}

func (h Handler) GetModelCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.modelRegistryAdmin(w, r); !ok {
		return
	}
	catalog := h.models.Current(r.Context())
	if r.URL.RawQuery == "" {
		writeJSON(w, http.StatusOK, catalog)
		return
	}
	query, valid := parseResourceQuery(r, map[string]bool{"model": true, "provider": true, "input_cost": true, "output_cost": true})
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid catalog query")
		return
	}
	providerFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("provider")))
	capabilityFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("capability")))
	models := catalog.Models[:0:0]
	for _, entry := range catalog.Models {
		haystack := strings.ToLower(entry.Provider + " " + entry.Model + " " + strings.Join(entry.Capabilities, " "))
		if query.Search != "" && !strings.Contains(haystack, query.Search) || providerFilter != "" && strings.ToLower(entry.Provider) != providerFilter || capabilityFilter != "" && !containsFold(entry.Capabilities, capabilityFilter) {
			continue
		}
		models = append(models, entry)
	}
	lessModel := func(i, j int) bool {
		switch query.Sort {
		case "provider":
			return strings.ToLower(models[i].Provider) < strings.ToLower(models[j].Provider)
		case "input_cost":
			return models[i].InputCostPer1M < models[j].InputCostPer1M
		case "output_cost":
			return models[i].OutputCostPer1M < models[j].OutputCostPer1M
		default:
			return strings.ToLower(models[i].Model) < strings.ToLower(models[j].Model)
		}
	}
	sort.SliceStable(models, func(i, j int) bool {
		if query.Order == "desc" {
			return lessModel(j, i)
		}
		return lessModel(i, j)
	})
	total := len(models)
	start, end := pageBounds(total, query.Offset, query.Limit)
	writeJSON(w, http.StatusOK, map[string]any{"data": models[start:end], "total": total, "limit": query.Limit, "offset": query.Offset, "version": catalog.Version, "unknown_model_policy": catalog.UnknownModelPolicy})
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}

func (h Handler) PutModelCatalog(w http.ResponseWriter, r *http.Request) {
	audit, ok := h.modelRegistryAdmin(w, r)
	if !ok {
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil || len(payload) == 0 || len(payload) > 1<<20 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid model catalog")
		return
	}
	catalog, err := modelcatalog.Parse(string(payload))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(catalog.Version) == "" || len(catalog.Version) > 128 || catalog.Models == nil || len(catalog.Models) > 5000 {
		writeError(w, http.StatusBadRequest, "invalid_request", "runtime model catalog requires a bounded version and models")
		return
	}
	for _, entry := range catalog.Models {
		if len(entry.Provider) > 256 || len(entry.Model) > 256 {
			writeError(w, http.StatusBadRequest, "invalid_request", "model catalog entry is too long")
			return
		}
		seenCapabilities := map[string]bool{}
		for _, capability := range entry.Capabilities {
			if !provider.ValidModelCapability(capability) || seenCapabilities[capability] {
				writeError(w, http.StatusBadRequest, "invalid_request", "invalid model capability")
				return
			}
			seenCapabilities[capability] = true
		}
		if entry.Currency != "" && (len(entry.Currency) != 3 || entry.Currency != strings.ToUpper(entry.Currency)) {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid model currency")
			return
		}
		if (entry.InputCostPer1M > 0 || entry.OutputCostPer1M > 0 || entry.SearchCostPer1K > 0 || entry.CharacterCostPer1M > 0 || entry.PageCostPer1K > 0 || entry.AudioCostPerMinute > 0 || entry.VideoCostPerSecond > 0) && entry.Currency == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "priced runtime catalog entries require currency")
			return
		}
	}
	event := AuditEvent{Action: "model_catalog.replace", TargetType: "model_catalog", TargetID: catalog.Version}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	var updateErr error
	if controller, available := h.provider.(provider.ModelCatalogController); available {
		_, updateErr = controller.UpdateModelCatalog(r.Context(), catalog)
	} else {
		updateErr = h.models.Update(r.Context(), catalog)
	}
	if updateErr != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		if errors.Is(updateErr, provider.ErrControlPlaneConflict) {
			writeError(w, http.StatusConflict, "revision_conflict", "control plane changed; refresh and retry")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "model_registry_unavailable", "model registry is unavailable")
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusOK, catalog)
}

func (h Handler) modelRegistryAdmin(w http.ResponseWriter, r *http.Request) (ManagementAudit, bool) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return ManagementAudit{}, false
	}
	if h.models == nil {
		writeError(w, http.StatusServiceUnavailable, "model_registry_unavailable", "model registry is not configured")
		return ManagementAudit{}, false
	}
	return managementAudit(req), true
}
