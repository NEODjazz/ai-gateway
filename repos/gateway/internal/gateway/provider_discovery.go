package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"ai-gateway-gateway/internal/provider"
)

func (h Handler) providerDiscoveryController() (provider.ProviderDiscoveryController, bool) {
	controller, ok := h.provider.(provider.ProviderDiscoveryController)
	return controller, ok && controller != nil
}

func providerCredentialID(w http.ResponseWriter, r *http.Request) (string, bool) {
	var input struct {
		CredentialID string `json:"credential_id,omitempty"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid provider probe")
		return "", false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid provider probe")
		return "", false
	}
	return input.CredentialID, true
}

func (h Handler) TestProviderConnection(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	controller, ok := h.providerDiscoveryController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "provider discovery is unavailable")
		return
	}
	credentialID, ok := providerCredentialID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	probe, err := controller.TestProvider(ctx, r.PathValue("id"), credentialID)
	if err != nil {
		writeProviderDiscoveryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, probe)
}

func (h Handler) DiscoverProviderModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	controller, ok := h.providerDiscoveryController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "provider discovery is unavailable")
		return
	}
	credentialID, ok := providerCredentialID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	models, err := controller.DiscoverProviderModels(ctx, r.PathValue("id"), credentialID)
	if err != nil {
		writeProviderDiscoveryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": models})
}

func writeProviderDiscoveryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, provider.ErrProviderNotFound):
		writeError(w, http.StatusNotFound, "not_found", "provider not found")
	case errors.Is(err, provider.ErrCredentialNotFound):
		writeError(w, http.StatusBadRequest, "invalid_credential", "credential not found")
	default:
		writeError(w, http.StatusBadGateway, "provider_unavailable", "provider connection failed")
	}
}
