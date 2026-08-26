package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"ai-gateway-gateway/internal/provider"
)

func (h Handler) GetRoutingDiagnostics(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	diagnostics, ok := h.provider.(provider.DiagnosticsProvider)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "diagnostics_unavailable", "routing diagnostics are unavailable")
		return
	}
	writeJSON(w, http.StatusOK, diagnostics.Diagnostics(r.Context()))
}

func (h Handler) SimulateRouting(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	controller, ok := h.provider.(provider.RoutingSimulationController)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "diagnostics_unavailable", "routing simulation is unavailable")
		return
	}
	var input provider.RoutingSimulationRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.Model) == "" || len(input.Model) > 256 || len(input.Provider) > 128 || input.MaxOutputTokens < 0 || len(input.Capabilities) > 32 || !validRoutingCapabilities(input.Capabilities) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid routing simulation")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid routing simulation")
		return
	}
	writeJSON(w, http.StatusOK, controller.SimulateRouting(r.Context(), input))
}

func validRoutingCapabilities(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > 256 {
			return false
		}
	}
	return true
}
