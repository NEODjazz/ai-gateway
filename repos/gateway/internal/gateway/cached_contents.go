package gateway

import (
	"context"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai-gateway-gateway/internal/cachedstate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

const cachedContentOwnerQuota = 1000

var (
	errCachedContentReferenceInvalid     = errors.New("cached content reference is invalid")
	errCachedContentReferenceNotFound    = errors.New("cached content reference not found")
	errCachedContentReferenceUnavailable = errors.New("cached content reference storage is unavailable")
	errCachedContentModelMismatch        = errors.New("cached content model does not match request model")
)

type cachedContentCreateRequest struct {
	generateRequest
	Model       string `json:"model"`
	DisplayName string `json:"displayName,omitempty"`
	TTL         string `json:"ttl,omitempty"`
	ExpireTime  string `json:"expireTime,omitempty"`
}

func (h Handler) WithCachedContentStore(store cachedstate.Store) Handler {
	h.cachedContents = store
	return h
}

func (h Handler) resolveCachedContentReference(ctx context.Context, identity modules.RequestContext, request *openai.ChatCompletionRequest) error {
	if request == nil || request.GeminiCachedContent == "" {
		return nil
	}
	if !validCachedContentName(request.GeminiCachedContent) {
		return errCachedContentReferenceInvalid
	}
	if h.cachedContents == nil {
		return errCachedContentReferenceUnavailable
	}
	record, err := h.cachedContents.GetCachedContentRecord(ctx, fileOwnerKey(identity), request.GeminiCachedContent)
	if errors.Is(err, cachedstate.ErrNotFound) {
		return errCachedContentReferenceNotFound
	}
	if err != nil {
		return errCachedContentReferenceUnavailable
	}
	if record.Binding.Model != request.Model {
		return errCachedContentModelMismatch
	}
	if record.Content.UsageMetadata == nil || record.Content.UsageMetadata.TotalTokenCount < 0 || record.Binding.Endpoint == "" || len(record.Binding.Deployment) != 64 || len(record.Binding.Policy) != 64 {
		return errCachedContentReferenceUnavailable
	}
	request.GeminiCachedContent = record.Content.Name
	request.GeminiCachedContentEndpoint = record.Binding.Endpoint
	request.GeminiCachedContentDeployment = record.Binding.Deployment
	request.GeminiCachedContentPolicy = record.Binding.Policy
	request.NativeInputTokens = openai.ReserveTokens(request.NativeInputTokens, record.Content.UsageMetadata.TotalTokenCount)
	return nil
}

func writeCachedContentReferenceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errCachedContentReferenceNotFound):
		writeError(w, http.StatusNotFound, "cached_content_not_found", "cached content not found")
	case errors.Is(err, errCachedContentReferenceInvalid), errors.Is(err, errCachedContentModelMismatch):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		writeError(w, http.StatusServiceUnavailable, "cached_content_unavailable", "cached content storage is unavailable")
	}
}

func (h Handler) CreateCachedContent(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	var input cachedContentCreateRequest
	if !decodeInferenceRequest(w, r, &input) {
		return
	}
	request, ok := cachedContentAuthenticatedRequest(w, r)
	if !ok {
		return
	}
	identity, ok := h.authorizeOwnedStorageOperation(w, request, "cached_content")
	if !ok {
		return
	}
	if h.cachedContents == nil {
		writeError(w, http.StatusServiceUnavailable, "cached_content_unavailable", "cached content storage is unavailable")
		return
	}
	runtime, ok := h.provider.(provider.CachedContentProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "unsupported_operation", "cached content is not supported")
		return
	}
	model, ok := publicCachedContentModel(input.Model)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "model must be a valid models/{model} resource name")
		return
	}
	chat, err := input.generateRequest.cachedContentChat(model)
	if err != nil || !validCachedContentExpiration(openai.GeminiCachedContentExpiration{TTL: input.TTL, ExpireTime: input.ExpireTime}, time.Now()) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid cached content request")
		return
	}
	if !h.authorizeBatchModel(w, identity, model) || !h.applyPolicyAttachments(w, &identity, model) {
		return
	}
	pipeline := h.resourceBillingPipeline()
	var attempt *modules.RequestContext
	content, binding, err := runtime.CreateCachedContent(r.Context(), identity, chat, input.DisplayName, openai.GeminiCachedContentExpiration{TTL: input.TTL, ExpireTime: input.ExpireTime}, func(ctx context.Context, current *modules.RequestContext) error {
		attempt = current
		return pipeline.RunAfterAuthentication(ctx, current)
	})
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	expiresAt, parseErr := time.Parse(time.RFC3339Nano, content.ExpireTime)
	if parseErr != nil || !expiresAt.After(time.Now()) {
		err = errors.New("provider returned invalid cached content expiration")
		h.compensateCachedContent(r.Context(), runtime, binding, content.Name, "", false, attempt, err)
		writeProviderFailure(w, err)
		return
	}
	owner := fileOwnerKey(identity)
	created, err := h.cachedContents.CreateCachedContentRecord(r.Context(), cachedstate.Record{OwnerKey: owner, Binding: binding, Content: content, ExpiresAt: expiresAt}, cachedContentOwnerQuota)
	if err != nil {
		h.compensateCachedContent(r.Context(), runtime, binding, content.Name, owner, false, attempt, err)
		writeCachedContentStoreError(w, err)
		return
	}
	if attempt != nil {
		if err = pipeline.RunPostResponse(r.Context(), attempt); err != nil {
			h.compensateCachedContent(r.Context(), runtime, binding, content.Name, owner, true, attempt, err)
			writeProviderFailure(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, created.Content)
}

func (h Handler) ListCachedContents(w http.ResponseWriter, r *http.Request) {
	owner, _, ok := h.cachedContentStorageOwner(w, r)
	if !ok {
		return
	}
	limit, after, ok := cachedContentListOptions(w, r)
	if !ok {
		return
	}
	records, next, err := h.cachedContents.ListCachedContentRecords(r.Context(), owner, limit, after)
	if err != nil {
		writeCachedContentStoreError(w, err)
		return
	}
	data := make([]openai.GeminiCachedContent, len(records))
	for index := range records {
		data[index] = records[index].Content
	}
	response := map[string]any{"cachedContents": data}
	if next != "" {
		response["nextPageToken"] = encodeCachedContentCursor(next)
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) GetCachedContent(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	owner, runtime, _, ok := h.cachedContentOwner(w, r)
	if !ok {
		return
	}
	record, ok := h.cachedContentRecord(w, r, owner)
	if !ok {
		return
	}
	content, err := runtime.RetrieveCachedContent(r.Context(), record.Binding, record.Content.Name)
	if err != nil {
		if cachedContentProviderNotFound(err) {
			_ = h.cachedContents.DeleteCachedContentRecord(r.Context(), owner, record.Content.Name)
			writeError(w, http.StatusNotFound, "cached_content_not_found", "cached content not found")
			return
		}
		writeCachedContentProviderError(w, err)
		return
	}
	updated, err := h.cachedContents.UpdateCachedContentRecord(r.Context(), owner, content)
	if err != nil {
		writeCachedContentStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated.Content)
}

func (h Handler) UpdateCachedContent(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 1 || len(query["updateMask"]) != 1 || query.Get("updateMask") != "ttl" && query.Get("updateMask") != "expireTime" {
		writeError(w, http.StatusBadRequest, "invalid_request", "updateMask must be ttl or expireTime")
		return
	}
	var expiration openai.GeminiCachedContentExpiration
	if !decodeInferenceRequest(w, r, &expiration) {
		return
	}
	mask := query.Get("updateMask")
	if mask == "ttl" && (expiration.TTL == "" || expiration.ExpireTime != "") || mask == "expireTime" && (expiration.ExpireTime == "" || expiration.TTL != "") || !validCachedContentExpiration(expiration, time.Now()) {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must contain only the expiration selected by updateMask")
		return
	}
	owner, runtime, _, ok := h.cachedContentOwner(w, r)
	if !ok {
		return
	}
	record, ok := h.cachedContentRecord(w, r, owner)
	if !ok {
		return
	}
	content, err := runtime.UpdateCachedContent(r.Context(), record.Binding, record.Content.Name, expiration)
	if err != nil {
		writeCachedContentProviderError(w, err)
		return
	}
	updated, err := h.cachedContents.UpdateCachedContentRecord(r.Context(), owner, content)
	if err != nil {
		h.restoreCachedContentExpiration(r.Context(), runtime, record)
		writeCachedContentStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated.Content)
}

func (h Handler) DeleteCachedContent(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	owner, runtime, _, ok := h.cachedContentOwner(w, r)
	if !ok {
		return
	}
	record, ok := h.cachedContentRecord(w, r, owner)
	if !ok {
		return
	}
	err := runtime.DeleteCachedContent(r.Context(), record.Binding, record.Content.Name)
	if err != nil && !cachedContentProviderNotFound(err) {
		writeCachedContentProviderError(w, err)
		return
	}
	if err = h.cachedContents.DeleteCachedContentRecord(r.Context(), owner, record.Content.Name); err != nil {
		writeCachedContentStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func cachedContentAuthenticatedRequest(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	bearer := bearerToken(r.Header.Get("Authorization"))
	native := strings.TrimSpace(r.Header.Get("x-goog-api-key"))
	if native != "" && bearer != "" && native != bearer {
		writeError(w, http.StatusBadRequest, "invalid_request", "conflicting authentication headers")
		return nil, false
	}
	if native == "" {
		return r, true
	}
	copyRequest := r.Clone(r.Context())
	copyRequest.Header.Set("Authorization", "Bearer "+native)
	return copyRequest, true
}

func (h Handler) cachedContentOwner(w http.ResponseWriter, r *http.Request) (string, provider.CachedContentProvider, modules.RequestContext, bool) {
	owner, identity, ok := h.cachedContentStorageOwner(w, r)
	if !ok {
		return "", nil, modules.RequestContext{}, false
	}
	runtime, ok := h.provider.(provider.CachedContentProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "unsupported_operation", "cached content is not supported")
		return "", nil, modules.RequestContext{}, false
	}
	return owner, runtime, identity, true
}

func (h Handler) cachedContentStorageOwner(w http.ResponseWriter, r *http.Request) (string, modules.RequestContext, bool) {
	request, ok := cachedContentAuthenticatedRequest(w, r)
	if !ok {
		return "", modules.RequestContext{}, false
	}
	identity, ok := h.authorizeOwnedStorageOperation(w, request, "cached_content")
	if !ok {
		return "", modules.RequestContext{}, false
	}
	if h.cachedContents == nil {
		writeError(w, http.StatusServiceUnavailable, "cached_content_unavailable", "cached content storage is unavailable")
		return "", modules.RequestContext{}, false
	}
	return fileOwnerKey(identity), identity, true
}

func (h Handler) cachedContentRecord(w http.ResponseWriter, r *http.Request, owner string) (cachedstate.Record, bool) {
	name := "cachedContents/" + r.PathValue("id")
	if !validCachedContentName(name) {
		writeError(w, http.StatusBadRequest, "invalid_request", "cached content name is invalid")
		return cachedstate.Record{}, false
	}
	record, err := h.cachedContents.GetCachedContentRecord(r.Context(), owner, name)
	if err != nil {
		writeCachedContentStoreError(w, err)
		return cachedstate.Record{}, false
	}
	return record, true
}

func cachedContentListOptions(w http.ResponseWriter, r *http.Request) (int, string, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid query parameters")
		return 0, "", false
	}
	for key, values := range query {
		if key != "pageSize" && key != "pageToken" || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "unsupported or repeated query parameter "+key)
			return 0, "", false
		}
	}
	limit := 20
	if raw := query.Get("pageSize"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, http.StatusBadRequest, "invalid_request", "pageSize must be between 1 and 100")
			return 0, "", false
		}
	}
	after := ""
	if token := query.Get("pageToken"); token != "" {
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(token)
		after = string(decoded)
		if decodeErr != nil || !validCachedContentName(after) || encodeCachedContentCursor(after) != token {
			writeError(w, http.StatusBadRequest, "invalid_request", "pageToken is invalid")
			return 0, "", false
		}
	}
	return limit, after, true
}

func encodeCachedContentCursor(name string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(name))
}

func publicCachedContentModel(value string) (string, bool) {
	if !strings.HasPrefix(value, "models/") {
		return "", false
	}
	model := strings.TrimPrefix(value, "models/")
	return model, validCachedContentModel(model)
}

func validCachedContentModel(value string) bool {
	if value == "" || len(value) > 256 || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("_.:-", character) {
			continue
		}
		return false
	}
	return true
}

func validCachedContentName(value string) bool {
	if !strings.HasPrefix(value, "cachedContents/") || len(value) > 256 {
		return false
	}
	id := strings.TrimPrefix(value, "cachedContents/")
	return id != "." && id != ".." && validFileToken(id, 241)
}

func validCachedContentExpiration(expiration openai.GeminiCachedContentExpiration, now time.Time) bool {
	if (expiration.TTL == "") == (expiration.ExpireTime == "") {
		return false
	}
	if expiration.TTL != "" {
		if !strings.HasSuffix(expiration.TTL, "s") || strings.ContainsAny(strings.TrimSuffix(expiration.TTL, "s"), "+-") {
			return false
		}
		duration, err := time.ParseDuration(expiration.TTL)
		return err == nil && duration > 0
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expiration.ExpireTime)
	return err == nil && expiresAt.After(now)
}

func cachedContentProviderNotFound(err error) bool {
	var providerErr *provider.Error
	return errors.As(err, &providerErr) && providerErr.StatusCode == http.StatusNotFound
}

func writeCachedContentProviderError(w http.ResponseWriter, err error) {
	if errors.Is(err, provider.ErrCachedContentDeploymentChanged) {
		writeError(w, http.StatusConflict, "cached_content_deployment_changed", "cached content deployment has changed")
		return
	}
	if cachedContentProviderNotFound(err) {
		writeError(w, http.StatusNotFound, "cached_content_not_found", "cached content not found")
		return
	}
	writeProviderFailure(w, err)
}

func writeCachedContentStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, cachedstate.ErrNotFound):
		writeError(w, http.StatusNotFound, "cached_content_not_found", "cached content not found")
	case errors.Is(err, cachedstate.ErrQuotaExceeded):
		writeError(w, http.StatusTooManyRequests, "cached_content_limit_exceeded", "too many cached content resources")
	case errors.Is(err, cachedstate.ErrConflict):
		writeError(w, http.StatusConflict, "cached_content_conflict", "cached content already exists")
	default:
		writeError(w, http.StatusServiceUnavailable, "cached_content_unavailable", "cached content storage is unavailable")
	}
}

func (h Handler) compensateCachedContent(ctx context.Context, runtime provider.CachedContentProvider, binding provider.CachedContentBinding, name, owner string, stored bool, request *modules.RequestContext, cause error) {
	compensation, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := runtime.DeleteCachedContent(compensation, binding, name); err != nil && !cachedContentProviderNotFound(err) {
		log.Printf("cached content compensation delete failed for %s: %v", name, err)
	} else if stored {
		if err := h.cachedContents.DeleteCachedContentRecord(compensation, owner, name); err != nil && !errors.Is(err, cachedstate.ErrNotFound) {
			log.Printf("cached content compensation ownership cleanup failed for %s: %v", name, err)
		}
	}
	if request != nil {
		h.resourceBillingPipeline().RunFailure(compensation, request, cause)
	}
}

func (h Handler) restoreCachedContentExpiration(ctx context.Context, runtime provider.CachedContentProvider, record cachedstate.Record) {
	compensation, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := runtime.UpdateCachedContent(compensation, record.Binding, record.Content.Name, openai.GeminiCachedContentExpiration{ExpireTime: record.Content.ExpireTime}); err != nil {
		log.Printf("cached content expiration compensation failed for %s: %v", record.Content.Name, err)
	}
}
