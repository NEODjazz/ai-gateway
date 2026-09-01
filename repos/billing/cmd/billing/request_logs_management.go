package main

import (
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-gateway-billing/internal/modules"
)

type requestLogManagementHandler struct {
	reporter modules.RequestLogReporter
	initErr  error
	secret   string
}

func registerRequestLogManagement(mux *http.ServeMux, reporter modules.RequestLogReporter, initErr error, secret string) {
	h := requestLogManagementHandler{reporter: reporter, initErr: initErr, secret: strings.TrimSpace(secret)}
	mux.HandleFunc("GET /internal/v1/request-logs", h.list)
	mux.HandleFunc("GET /internal/v1/request-logs/settings", h.settings)
	mux.HandleFunc("GET /internal/v1/request-logs/{request_id}", h.get)
}

func (h requestLogManagementHandler) authorize(w http.ResponseWriter, r *http.Request) bool {
	provided := r.Header.Get("X-Management-Token")
	if h.secret == "" || len(provided) != len(h.secret) || subtle.ConstantTimeCompare([]byte(provided), []byte(h.secret)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if h.reporter == nil || h.initErr != nil {
		http.Error(w, "request logs unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (h requestLogManagementHandler) list(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	days, err := strconv.Atoi(defaultValue(r.URL.Query().Get("days"), "7"))
	limit, limitErr := modules.ParseRequestLogLimit(r.URL.Query().Get("limit"), 100)
	if err != nil || days < 1 || days > 90 || limitErr != nil {
		http.Error(w, "invalid request log filter", http.StatusBadRequest)
		return
	}
	filter := modules.RequestLogFilter{Days: days, Limit: limit}
	for name, target := range map[string]*string{
		"request_id": &filter.RequestID, "session_id": &filter.SessionID, "trace_id": &filter.TraceID, "status": &filter.Status, "model": &filter.Model, "provider": &filter.Provider, "tag": &filter.Tag,
		"user_id": &filter.UserID, "team_id": &filter.TeamID, "organization_id": &filter.OrganizationID, "credential_id": &filter.CredentialID, "cache_status": &filter.CacheStatus,
	} {
		*target = strings.TrimSpace(r.URL.Query().Get(name))
		if len(*target) > 256 {
			http.Error(w, "invalid request log filter", http.StatusBadRequest)
			return
		}
	}
	if filter.CacheStatus != "" && filter.CacheStatus != "hit" && filter.CacheStatus != "miss" && filter.CacheStatus != "error" {
		http.Error(w, "invalid request log filter", http.StatusBadRequest)
		return
	}
	if filter.TraceID != "" {
		if len(filter.TraceID) != 32 {
			http.Error(w, "invalid request log filter", http.StatusBadRequest)
			return
		}
		if _, err := hex.DecodeString(filter.TraceID); err != nil {
			http.Error(w, "invalid request log filter", http.StatusBadRequest)
			return
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("before")); raw != "" {
		filter.Before, err = time.Parse(time.RFC3339Nano, raw)
		filter.BeforeRequestID = strings.TrimSpace(r.URL.Query().Get("before_request_id"))
		if err != nil || filter.BeforeRequestID == "" || len(filter.BeforeRequestID) > 256 {
			http.Error(w, "invalid request log filter", http.StatusBadRequest)
			return
		}
	}
	page, err := h.reporter.ListRequestLogs(r.Context(), filter)
	if err != nil {
		http.Error(w, "request logs unavailable", http.StatusServiceUnavailable)
		return
	}
	writeBudgetJSON(w, http.StatusOK, page)
}

func (h requestLogManagementHandler) get(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	requestID := strings.TrimSpace(r.PathValue("request_id"))
	if requestID == "" || len(requestID) > 256 {
		http.Error(w, "invalid request id", http.StatusBadRequest)
		return
	}
	row, err := h.reporter.GetRequestLog(r.Context(), requestID)
	if errors.Is(err, modules.ErrRequestLogNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "request logs unavailable", http.StatusServiceUnavailable)
		return
	}
	writeBudgetJSON(w, http.StatusOK, row)
}

func (h requestLogManagementHandler) settings(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	writeBudgetJSON(w, http.StatusOK, h.reporter.RequestLogSettings())
}

func defaultValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
