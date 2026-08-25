package gateway

import (
	"net/http"

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
