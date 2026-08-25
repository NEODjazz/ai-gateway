package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"ai-gateway-auth/internal/modules"
)

const managementTokenHeader = "X-Management-Token"

func registerManagementRoutes(mux *http.ServeMux, module *modules.AuthModule, sharedSecret string) {
	mux.HandleFunc("GET /internal/v1/keys", managementAuthorized(sharedSecret, func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 500 {
				http.Error(w, "invalid virtual key list limit", http.StatusBadRequest)
				return
			}
			limit = parsed
		}
		keys, err := module.ListVirtualKeys(r.Context(), limit)
		if errors.Is(err, modules.ErrInvalidVirtualKey) {
			http.Error(w, "invalid virtual key list limit", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "virtual key listing failed", http.StatusServiceUnavailable)
			return
		}
		writeManagementJSON(w, http.StatusOK, map[string]any{"data": keys})
	}))
	mux.HandleFunc("POST /internal/v1/keys", managementAuthorized(sharedSecret, func(w http.ResponseWriter, r *http.Request) {
		spec, ok := decodeManagedVirtualKey(w, r)
		if !ok {
			return
		}
		issued, err := module.CreateVirtualKey(r.Context(), spec)
		if errors.Is(err, modules.ErrInvalidVirtualKey) {
			http.Error(w, "invalid virtual key policy", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "virtual key creation failed", http.StatusServiceUnavailable)
			return
		}
		logManagementAction(r, "virtual_key.create", issued.ID)
		writeManagementJSON(w, http.StatusCreated, issued)
	}))
	mux.HandleFunc("POST /internal/v1/keys/{id}/rotate", managementAuthorized(sharedSecret, func(w http.ResponseWriter, r *http.Request) {
		spec, ok := decodeManagedVirtualKey(w, r)
		if !ok {
			return
		}
		issued, err := module.RotateVirtualKey(r.Context(), r.PathValue("id"), spec)
		if errors.Is(err, modules.ErrInvalidVirtualKey) {
			http.Error(w, "invalid virtual key policy", http.StatusBadRequest)
			return
		}
		if errors.Is(err, modules.ErrVirtualKeyNotFound) {
			http.Error(w, "virtual key not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "virtual key rotation failed", http.StatusServiceUnavailable)
			return
		}
		logManagementAction(r, "virtual_key.rotate", issued.ID)
		writeManagementJSON(w, http.StatusCreated, issued)
	}))
	mux.HandleFunc("DELETE /internal/v1/keys/{id}", managementAuthorized(sharedSecret, func(w http.ResponseWriter, r *http.Request) {
		revoked, err := module.RevokeVirtualKey(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, "virtual key revocation failed", http.StatusServiceUnavailable)
			return
		}
		if !revoked {
			http.Error(w, "virtual key not found", http.StatusNotFound)
			return
		}
		logManagementAction(r, "virtual_key.revoke", r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	}))
}

func managementAuthorized(sharedSecret string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provided := r.Header.Get(managementTokenHeader)
		expectedHash := sha256.Sum256([]byte(sharedSecret))
		providedHash := sha256.Sum256([]byte(provided))
		if sharedSecret == "" || subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if strings.TrimSpace(r.Header.Get("X-Request-ID")) == "" || strings.TrimSpace(r.Header.Get("X-Actor-ID")) == "" || strings.TrimSpace(r.Header.Get("X-Actor-Credential-ID")) == "" {
			http.Error(w, "missing audit identity", http.StatusBadRequest)
			return
		}
		next(w, r)
	}
}

func decodeManagedVirtualKey(w http.ResponseWriter, r *http.Request) (modules.ManagedVirtualKey, bool) {
	var spec modules.ManagedVirtualKey
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		http.Error(w, "invalid virtual key policy", http.StatusBadRequest)
		return spec, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "invalid virtual key policy", http.StatusBadRequest)
		return spec, false
	}
	return spec, true
}

func logManagementAction(r *http.Request, action, target string) {
	payload, _ := json.Marshal(map[string]string{
		"event": "management_action", "request_id": r.Header.Get("X-Request-ID"),
		"actor_id": r.Header.Get("X-Actor-ID"), "actor_credential_id": r.Header.Get("X-Actor-Credential-ID"),
		"action": action, "target_id": target,
	})
	log.Print(string(payload))
}

func writeManagementJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
