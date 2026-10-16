package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
)

// GlobalRateLimitSettings limits aggregate inference admission across identities.
// Zero disables that dimension. Credential and deployment limits still apply.
type GlobalRateLimitSettings struct {
	RPM int `json:"rpm"`
	TPM int `json:"tpm"`
}

func (s GlobalRateLimitSettings) valid() bool {
	return s.RPM >= 0 && s.RPM <= 10_000_000 && s.TPM >= 0 && s.TPM <= 1_000_000_000
}

type GlobalRateLimits struct {
	mu       sync.RWMutex
	defaults GlobalRateLimitSettings
	override *GlobalRateLimitSettings
}

func NewGlobalRateLimits(defaults GlobalRateLimitSettings) (*GlobalRateLimits, error) {
	if !defaults.valid() {
		return nil, errors.New("invalid global rate limits")
	}
	return &GlobalRateLimits{defaults: defaults}, nil
}

func (s *GlobalRateLimits) snapshot() *GlobalRateLimitSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.override == nil {
		return nil
	}
	value := *s.override
	return &value
}

func (s *GlobalRateLimits) apply(value *GlobalRateLimitSettings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.override = nil
	if value != nil {
		copy := *value
		s.override = &copy
	}
}

func (s *GlobalRateLimits) effective() GlobalRateLimitSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.override != nil {
		return *s.override
	}
	return s.defaults
}

func (h Handler) WithGlobalRateLimits(settings *GlobalRateLimits) Handler {
	h.globalRateLimits = settings
	return h
}

func (h Handler) GlobalRateLimitSettings(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.globalRateLimits == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "global rate limits are not configured")
		return
	}
	if r.Method != http.MethodGet {
		var value *GlobalRateLimitSettings
		if r.Method == http.MethodPut {
			// Both fields are required: a partial form must not disable a limit.
			var input struct {
				RPM *int `json:"rpm"`
				TPM *int `json:"tpm"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&input) != nil || input.RPM == nil || input.TPM == nil || decoder.Decode(new(any)) != io.EOF {
				writeError(w, http.StatusBadRequest, "invalid_request", "rpm and tpm must be non-negative integers")
				return
			}
			value = &GlobalRateLimitSettings{RPM: *input.RPM, TPM: *input.TPM}
			if !value.valid() {
				writeError(w, http.StatusBadRequest, "invalid_request", "rpm must be 0–10000000 and tpm 0–1000000000")
				return
			}
		}
		event := AuditEvent{Action: "global_rate_limits.update", TargetType: "settings", TargetID: "global_rate_limits"}
		if !h.auditMutation(r.Context(), managementAudit(actor), event) {
			writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
			return
		}
		h.globalRateLimits.apply(value)
		h.auditOutcome(r.Context(), managementAudit(actor), event, "succeeded")
	}
	source := "environment"
	if h.globalRateLimits.snapshot() != nil {
		source = "configuration"
	}
	writeJSON(w, http.StatusOK, map[string]any{"limits": h.globalRateLimits.effective(), "defaults": h.globalRateLimits.defaults, "source": source})
}
