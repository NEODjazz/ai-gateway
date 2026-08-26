package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ai-gateway-gateway/internal/modules"
)

type LoggingDestination struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Type             string   `json:"type"`
	URL              string   `json:"url"`
	EventTypes       []string `json:"event_types"`
	Enabled          bool     `json:"enabled"`
	SecretConfigured bool     `json:"secret_configured"`
}

type loggingDestinationEntry struct {
	destination LoggingDestination
	secret      string
}

type LoggingDeliveryStats struct {
	Queued    uint64 `json:"queued"`
	Delivered uint64 `json:"delivered"`
	Failed    uint64 `json:"failed"`
	Dropped   uint64 `json:"dropped"`
}

type loggingDelivery struct {
	eventType string
	payload   LoggingEvent
}

type LoggingRegistry struct {
	mu           sync.RWMutex
	destinations map[string]loggingDestinationEntry
	client       *http.Client
	queue        chan loggingDelivery
	queued       atomic.Uint64
	delivered    atomic.Uint64
	failed       atomic.Uint64
	dropped      atomic.Uint64
}

var errInvalidLoggingDestination = errors.New("invalid logging destination")

func NewLoggingRegistry(client *http.Client) *LoggingRegistry {
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	registry := &LoggingRegistry{destinations: map[string]loggingDestinationEntry{}, client: client, queue: make(chan loggingDelivery, 256)}
	go registry.run()
	return registry
}

func (r *LoggingRegistry) run() {
	for delivery := range r.queue {
		if err := r.deliver(context.Background(), delivery.eventType, delivery.payload); err != nil {
			r.failed.Add(1)
		} else {
			r.delivered.Add(1)
		}
	}
}

func (r *LoggingRegistry) Destinations() []LoggingDestination {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]LoggingDestination, 0, len(r.destinations))
	for _, entry := range r.destinations {
		item := entry.destination
		item.EventTypes = append([]string(nil), item.EventTypes...)
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (r *LoggingRegistry) Put(id string, destination LoggingDestination, secret string) (LoggingDestination, error) {
	id = strings.TrimSpace(id)
	destination.Name = strings.TrimSpace(destination.Name)
	destination.Type = strings.TrimSpace(destination.Type)
	parsed, err := url.Parse(strings.TrimSpace(destination.URL))
	if !validMCPID(id) || destination.Name == "" || len(destination.Name) > 256 || destination.Type != "webhook" || err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !validLoggingEventTypes(destination.EventTypes) || len(secret) > 32768 {
		return LoggingDestination{}, errInvalidLoggingDestination
	}
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	destination.ID = id
	destination.URL = parsed.String()
	destination.EventTypes = uniqueStrings(destination.EventTypes)
	r.mu.Lock()
	defer r.mu.Unlock()
	if previous, ok := r.destinations[id]; secret == "" && ok {
		secret = previous.secret
	}
	destination.SecretConfigured = secret != ""
	r.destinations[id] = loggingDestinationEntry{destination: destination, secret: secret}
	return destination, nil
}

func (r *LoggingRegistry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.destinations[id]; !ok {
		return errInvalidLoggingDestination
	}
	delete(r.destinations, id)
	return nil
}

func validLoggingEventTypes(values []string) bool {
	if len(values) == 0 || len(values) > 8 {
		return false
	}
	for _, value := range values {
		if value != "request_outcome" {
			return false
		}
	}
	return true
}

func (r *LoggingRegistry) Enqueue(eventType string, payload LoggingEvent) {
	if r == nil {
		return
	}
	r.mu.RLock()
	hasDestination := false
	for _, entry := range r.destinations {
		if entry.destination.Enabled && containsString(entry.destination.EventTypes, eventType) {
			hasDestination = true
			break
		}
	}
	r.mu.RUnlock()
	if !hasDestination {
		return
	}
	select {
	case r.queue <- loggingDelivery{eventType: eventType, payload: payload}:
		r.queued.Add(1)
	default:
		r.dropped.Add(1)
	}
}

func (r *LoggingRegistry) Stats() LoggingDeliveryStats {
	return LoggingDeliveryStats{Queued: r.queued.Load(), Delivered: r.delivered.Load(), Failed: r.failed.Load(), Dropped: r.dropped.Load()}
}

func (r *LoggingRegistry) deliver(ctx context.Context, eventType string, payload LoggingEvent) error {
	r.mu.RLock()
	entries := make([]loggingDestinationEntry, 0, len(r.destinations))
	for _, entry := range r.destinations {
		if entry.destination.Enabled && containsString(entry.destination.EventTypes, eventType) {
			entries = append(entries, entry)
		}
	}
	r.mu.RUnlock()
	var failures []error
	for _, entry := range entries {
		if err := r.send(ctx, entry, payload); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (r *LoggingRegistry) send(ctx context.Context, entry loggingDestinationEntry, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, entry.destination.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "ai-gateway-logging/1")
	if entry.secret != "" {
		request.Header.Set("Authorization", "Bearer "+entry.secret)
	}
	response, err := r.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("logging destination returned status %d", response.StatusCode)
	}
	return nil
}

type LoggingEvent struct {
	Event        string    `json:"event"`
	OccurredAt   time.Time `json:"occurred_at"`
	RequestID    string    `json:"request_id,omitempty"`
	SessionID    string    `json:"session_id,omitempty"`
	CredentialID string    `json:"credential_id,omitempty"`
	UserID       string    `json:"user_id,omitempty"`
	TeamID       string    `json:"team_id,omitempty"`
	Model        string    `json:"model,omitempty"`
	Provider     string    `json:"provider,omitempty"`
	Endpoint     string    `json:"endpoint,omitempty"`
	Status       string    `json:"status"`
	InputTokens  int       `json:"input_tokens,omitempty"`
	OutputTokens int       `json:"output_tokens,omitempty"`
	TotalTokens  int       `json:"total_tokens,omitempty"`
}

type loggingModule struct{ registry *LoggingRegistry }

func NewLoggingModule(registry *LoggingRegistry) modules.Module {
	return loggingModule{registry: registry}
}
func (loggingModule) Name() string                                          { return "logging" }
func (loggingModule) Required() bool                                        { return false }
func (loggingModule) Handle(context.Context, *modules.RequestContext) error { return nil }
func (loggingModule) PostResponseEnabled() bool                             { return true }
func (m loggingModule) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	m.registry.Enqueue("request_outcome", loggingEvent(req, "ok"))
	return nil
}
func (m loggingModule) HandleFailure(_ context.Context, req *modules.RequestContext, _ error) error {
	m.registry.Enqueue("request_outcome", loggingEvent(req, "error"))
	return nil
}

func loggingEvent(req *modules.RequestContext, status string) LoggingEvent {
	event := LoggingEvent{Event: "request_outcome", OccurredAt: time.Now().UTC(), RequestID: req.RequestID, SessionID: req.SessionID, CredentialID: req.CredentialID, UserID: req.UserID, TeamID: req.TeamID, Provider: req.Metadata["provider.id"], Endpoint: req.Metadata["provider.endpoint.name"], Status: status}
	event.Model = req.Request.Model
	if req.ResponseRequest != nil {
		event.Model = req.ResponseRequest.Model
	}
	if req.EmbeddingRequest != nil {
		event.Model = req.EmbeddingRequest.Model
	}
	if req.RerankRequest != nil {
		event.Model = req.RerankRequest.Model
	}
	if req.Usage != nil {
		event.InputTokens = req.Usage.PromptTokens
		event.OutputTokens = req.Usage.CompletionTokens
		event.TotalTokens = req.Usage.TotalTokens
	}
	return event
}

func (h Handler) WithLoggingRegistry(registry *LoggingRegistry) Handler {
	h.logging = registry
	return h
}

func (h Handler) ListLoggingDestinations(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	if h.logging == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "logging integrations are unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": h.logging.Destinations(), "delivery": h.logging.Stats(), "content_stored": false})
}

func (h Handler) PutLoggingDestination(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.logging == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "logging integrations are unavailable")
		return
	}
	var input struct {
		Name       string   `json:"name"`
		Type       string   `json:"type"`
		URL        string   `json:"url"`
		EventTypes []string `json:"event_types"`
		Enabled    bool     `json:"enabled"`
		Secret     string   `json:"secret,omitempty"`
	}
	if !decodeLoggingJSON(w, r, &input) {
		return
	}
	event := AuditEvent{Action: "logging_destination.update", TargetType: "logging_destination", TargetID: r.PathValue("id")}
	audit := managementAudit(req)
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	saved, err := h.logging.Put(event.TargetID, LoggingDestination{Name: input.Name, Type: input.Type, URL: input.URL, EventTypes: input.EventTypes, Enabled: input.Enabled}, input.Secret)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid logging destination")
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusOK, saved)
}

func (h Handler) DeleteLoggingDestination(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.logging == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "logging integrations are unavailable")
		return
	}
	event := AuditEvent{Action: "logging_destination.delete", TargetType: "logging_destination", TargetID: r.PathValue("id")}
	audit := managementAudit(req)
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	if err := h.logging.Delete(event.TargetID); err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeError(w, http.StatusNotFound, "not_found", "logging destination was not found")
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) TestLoggingDestination(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	if h.logging == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "logging integrations are unavailable")
		return
	}
	h.logging.mu.RLock()
	entry, found := h.logging.destinations[r.PathValue("id")]
	h.logging.mu.RUnlock()
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "logging destination was not found")
		return
	}
	started := time.Now()
	err := h.logging.send(r.Context(), entry, LoggingEvent{Event: "probe", OccurredAt: time.Now().UTC(), RequestID: "logging-probe", Status: "ok"})
	if err != nil {
		writeError(w, http.StatusBadGateway, "destination_unavailable", "logging destination probe failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "available", "latency_ms": time.Since(started).Milliseconds(), "content_sent": false})
}

func decodeLoggingJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid logging destination")
		return false
	}
	return true
}
