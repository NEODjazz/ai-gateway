package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
	"ai-gateway-gateway/internal/videostate"
)

const videoOwnerQuota = 1000
const maxGatewayVideoContentBytes = 512 << 20

func (h Handler) WithVideoStore(store videostate.Store) Handler {
	h.videos = store
	return h
}

func (h Handler) CreateVideo(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	var input openai.VideoCreateRequest
	if !decodeInferenceRequest(w, r, &input) {
		return
	}
	identity, ok := h.authorizeOwnedStorageOperation(w, r, "video")
	if !ok {
		return
	}
	if h.videos == nil {
		writeError(w, http.StatusServiceUnavailable, "video_unavailable", "video storage is unavailable")
		return
	}
	if input.Model == "" || len(input.Model) > 256 || len(input.Prompt) < 1 || len(input.Prompt) > 32000 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid video request")
		return
	}
	videoSeconds, err := billableVideoSeconds(input.Seconds, true)
	if err != nil {
		writeProviderParameterError(w, http.StatusBadRequest, "invalid_parameter", err.Error(), "seconds")
		return
	}
	if !h.authorizeBatchModel(w, identity, input.Model) {
		return
	}
	if input.InputReference != nil && input.InputReference.FileID != "" {
		writeProviderParameterError(w, http.StatusBadRequest, "unsupported_parameter", "local file references are not supported for video generation", "input_reference.file_id")
		return
	}
	runtime, ok := h.provider.(provider.VideoProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "unsupported_operation", "video generation is not supported")
		return
	}
	var billingRequest modules.RequestContext
	billingReserved := false
	billingErr := error(nil)
	video, binding, err := runtime.CreateVideo(r.Context(), identity, input, func(ctx context.Context, request *modules.RequestContext) error {
		billingRequest = *request
		billingRequest.VideoSeconds = videoSeconds
		billingErr = h.pipeline.RunBillingLifecycle(ctx, &billingRequest, "reserve", nil)
		billingReserved = billingErr == nil && h.pipeline.HasModule("billing")
		return billingErr
	})
	if err != nil {
		if billingReserved {
			h.cancelVideoBilling(r.Context(), &billingRequest, err)
		}
		if billingErr != nil {
			writeVideoBillingFailure(w, billingErr)
			return
		}
		writeProviderFailure(w, err)
		return
	}
	created, err := h.videos.CreateVideoRecord(r.Context(), videostate.Record{OwnerKey: fileOwnerKey(identity), Binding: binding, Video: video}, videoOwnerQuota)
	if err != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
		_, _ = runtime.DeleteVideo(ctx, binding, video.ID)
		cancel()
		if billingReserved {
			h.cancelVideoBilling(r.Context(), &billingRequest, err)
		}
		writeVideoStoreError(w, err)
		return
	}
	if billingReserved {
		if billingRequest.Metadata == nil {
			billingRequest.Metadata = map[string]string{}
		}
		billingRequest.Metadata["gateway.video_usage_exact"] = "true"
		if err = h.pipeline.RunBillingLifecycle(r.Context(), &billingRequest, "commit", nil); err != nil {
			compensation, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
			_, _ = runtime.DeleteVideo(compensation, binding, video.ID)
			_ = h.videos.DeleteVideoRecord(compensation, fileOwnerKey(identity), video.ID)
			cancel()
			h.cancelVideoBilling(r.Context(), &billingRequest, err)
			writeVideoBillingFailure(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, created.Video)
}

func (h Handler) ListVideos(w http.ResponseWriter, r *http.Request) {
	owner, _, _, ok := h.videoOwner(w, r)
	if !ok {
		return
	}
	options, ok := videoListOptions(w, r)
	if !ok {
		return
	}
	records, next, err := h.videos.ListVideoRecords(r.Context(), owner, options.Limit, options.After)
	if err != nil {
		writeVideoStoreError(w, err)
		return
	}
	data := make([]openai.Video, len(records))
	for i := range records {
		data[i] = records[i].Video
	}
	result := openai.VideoList{Object: "list", Data: data, HasMore: next != ""}
	if len(data) > 0 {
		result.FirstID, result.LastID = data[0].ID, data[len(data)-1].ID
	}
	writeJSON(w, http.StatusOK, result)
}

func (h Handler) GetVideo(w http.ResponseWriter, r *http.Request) {
	owner, runtime, _, ok := h.videoOwner(w, r)
	if !ok {
		return
	}
	record, ok := h.videoRecord(w, r, owner)
	if !ok {
		return
	}
	video, err := runtime.RetrieveVideo(r.Context(), record.Binding, record.Video.ID)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	updated, err := h.videos.UpdateVideoRecord(r.Context(), owner, video)
	if err != nil {
		writeVideoStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated.Video)
}

func (h Handler) DeleteVideo(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	owner, runtime, _, ok := h.videoOwner(w, r)
	if !ok {
		return
	}
	record, ok := h.videoRecord(w, r, owner)
	if !ok {
		return
	}
	deleted, err := runtime.DeleteVideo(r.Context(), record.Binding, record.Video.ID)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	if err = h.videos.DeleteVideoRecord(r.Context(), owner, record.Video.ID); err != nil {
		writeVideoStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, deleted)
}

func (h Handler) GetVideoContent(w http.ResponseWriter, r *http.Request) {
	owner, runtime, _, ok := h.videoOwner(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	for key, values := range q {
		if key != "variant" || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "unsupported or repeated query parameter "+key)
			return
		}
	}
	record, ok := h.videoRecord(w, r, owner)
	if !ok {
		return
	}
	content, err := runtime.DownloadVideoContent(r.Context(), record.Binding, record.Video.ID, q.Get("variant"))
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	defer content.Body.Close()
	w.Header().Set("Content-Type", content.ContentType)
	if content.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(content.ContentLength, 10))
	}
	if _, err = io.Copy(w, io.LimitReader(content.Body, maxGatewayVideoContentBytes+1)); err != nil {
		return
	}
}

func (h Handler) RemixVideo(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	var input openai.VideoRemixRequest
	if !decodeInferenceRequest(w, r, &input) {
		return
	}
	if len(input.Prompt) < 1 || len(input.Prompt) > 32000 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid remix prompt")
		return
	}
	owner, runtime, identity, ok := h.videoOwner(w, r)
	if !ok {
		return
	}
	record, ok := h.videoRecord(w, r, owner)
	if !ok {
		return
	}
	videoSeconds, err := billableVideoSeconds(record.Video.Seconds, false)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "video_unavailable", "stored video duration is invalid")
		return
	}
	var billingRequest modules.RequestContext
	billingReserved := false
	billingErr := error(nil)
	video, binding, err := runtime.RemixVideo(r.Context(), identity, record.Binding, record.Video.ID, input, func(ctx context.Context, request *modules.RequestContext) error {
		billingRequest = *request
		billingRequest.VideoSeconds = videoSeconds
		billingErr = h.pipeline.RunBillingLifecycle(ctx, &billingRequest, "reserve", nil)
		billingReserved = billingErr == nil && h.pipeline.HasModule("billing")
		return billingErr
	})
	if err != nil {
		if billingReserved {
			h.cancelVideoBilling(r.Context(), &billingRequest, err)
		}
		if billingErr != nil {
			writeVideoBillingFailure(w, billingErr)
			return
		}
		writeProviderFailure(w, err)
		return
	}
	created, err := h.videos.CreateVideoRecord(r.Context(), videostate.Record{OwnerKey: owner, Binding: binding, Video: video}, videoOwnerQuota)
	if err != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
		_, _ = runtime.DeleteVideo(ctx, binding, video.ID)
		cancel()
		if billingReserved {
			h.cancelVideoBilling(r.Context(), &billingRequest, err)
		}
		writeVideoStoreError(w, err)
		return
	}
	if billingReserved {
		if billingRequest.Metadata == nil {
			billingRequest.Metadata = map[string]string{}
		}
		billingRequest.Metadata["gateway.video_usage_exact"] = "true"
		if err = h.pipeline.RunBillingLifecycle(r.Context(), &billingRequest, "commit", nil); err != nil {
			compensation, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
			_, _ = runtime.DeleteVideo(compensation, binding, video.ID)
			_ = h.videos.DeleteVideoRecord(compensation, owner, video.ID)
			cancel()
			h.cancelVideoBilling(r.Context(), &billingRequest, err)
			writeVideoBillingFailure(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, created.Video)
}

func billableVideoSeconds(raw string, defaultValue bool) (int, error) {
	if raw == "" && defaultValue {
		return 4, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || (seconds != 4 && seconds != 8 && seconds != 12) {
		return 0, errors.New("seconds must be 4, 8, or 12")
	}
	return seconds, nil
}

func (h Handler) cancelVideoBilling(ctx context.Context, request *modules.RequestContext, cause error) {
	compensation, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_ = h.pipeline.RunBillingLifecycle(compensation, request, "cancel", cause)
}

func writeVideoBillingFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, modules.ErrBudgetExceeded) {
		writeError(w, http.StatusTooManyRequests, "budget_exceeded", "budget exceeded")
		return
	}
	writeError(w, http.StatusServiceUnavailable, "billing_unavailable", "billing is unavailable")
}

func (h Handler) videoOwner(w http.ResponseWriter, r *http.Request) (string, provider.VideoProvider, modules.RequestContext, bool) {
	identity, ok := h.authorizeOwnedStorageOperation(w, r, "video")
	if !ok {
		return "", nil, modules.RequestContext{}, false
	}
	if h.videos == nil {
		writeError(w, http.StatusServiceUnavailable, "video_unavailable", "video storage is unavailable")
		return "", nil, modules.RequestContext{}, false
	}
	runtime, ok := h.provider.(provider.VideoProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "unsupported_operation", "video generation is not supported")
		return "", nil, modules.RequestContext{}, false
	}
	return fileOwnerKey(identity), runtime, identity, true
}

func (h Handler) videoRecord(w http.ResponseWriter, r *http.Request, owner string) (videostate.Record, bool) {
	id := r.PathValue("id")
	if !validFileToken(id, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "video ID is invalid")
		return videostate.Record{}, false
	}
	record, err := h.videos.GetVideoRecord(r.Context(), owner, id)
	if err != nil {
		writeVideoStoreError(w, err)
		return videostate.Record{}, false
	}
	return record, true
}

func videoListOptions(w http.ResponseWriter, r *http.Request) (provider.VideoListOptions, bool) {
	q := r.URL.Query()
	for key, values := range q {
		if (key != "after" && key != "limit" && key != "order") || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "unsupported or repeated query parameter "+key)
			return provider.VideoListOptions{}, false
		}
	}
	options := provider.VideoListOptions{After: q.Get("after"), Limit: 20, Order: q.Get("order")}
	if options.After != "" && !validFileToken(options.After, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "after is invalid")
		return options, false
	}
	if options.Order != "" && options.Order != "desc" {
		writeError(w, http.StatusBadRequest, "invalid_request", "only descending order is supported")
		return options, false
	}
	if raw := q.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 100")
			return options, false
		}
		options.Limit = value
	}
	return options, true
}

func writeVideoStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, videostate.ErrNotFound):
		writeError(w, http.StatusNotFound, "video_not_found", "video not found")
	case errors.Is(err, videostate.ErrQuotaExceeded):
		writeError(w, http.StatusTooManyRequests, "video_limit_exceeded", "too many video jobs")
	case errors.Is(err, videostate.ErrConflict):
		writeError(w, http.StatusConflict, "video_conflict", "video job already exists")
	default:
		writeError(w, http.StatusServiceUnavailable, "video_unavailable", "video storage is unavailable")
	}
}
