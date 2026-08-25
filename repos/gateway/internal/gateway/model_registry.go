package gateway

import (
	"io"
	"net/http"
	"strings"

	"ai-gateway-gateway/internal/modelcatalog"
)

func (h Handler) WithModelRegistry(registry *modelcatalog.Registry) Handler {
	h.models = registry
	return h
}

func (h Handler) GetModelCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.modelRegistryAdmin(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, h.models.Current(r.Context()))
}

func (h Handler) PutModelCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.modelRegistryAdmin(w, r); !ok {
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
	allowedCapabilities := map[string]bool{"chat": true, "responses": true, "embeddings": true, "stream": true, "tools": true, "structured_output": true, "mcp": true, "vision": true}
	for _, entry := range catalog.Models {
		if len(entry.Provider) > 256 || len(entry.Model) > 256 {
			writeError(w, http.StatusBadRequest, "invalid_request", "model catalog entry is too long")
			return
		}
		seenCapabilities := map[string]bool{}
		for _, capability := range entry.Capabilities {
			if !allowedCapabilities[capability] || seenCapabilities[capability] {
				writeError(w, http.StatusBadRequest, "invalid_request", "invalid model capability")
				return
			}
			seenCapabilities[capability] = true
		}
		if entry.Currency != "" && (len(entry.Currency) != 3 || entry.Currency != strings.ToUpper(entry.Currency)) {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid model currency")
			return
		}
		if (entry.InputCostPer1M > 0 || entry.OutputCostPer1M > 0) && entry.Currency == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "priced runtime catalog entries require currency")
			return
		}
	}
	if err := h.models.Update(r.Context(), catalog); err != nil {
		writeError(w, http.StatusServiceUnavailable, "model_registry_unavailable", "model registry is unavailable")
		return
	}
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
