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
	if h.pipeline.HasModule("billing") {
		writeError(w, http.StatusNotImplemented, "video_billing_unsupported", "video creation requires duration billing support")
		return
	}
	if input.Model == "" || len(input.Model) > 256 || len(input.Prompt) < 1 || len(input.Prompt) > 32000 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid video request")
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
	video, binding, err := runtime.CreateVideo(r.Context(), identity, input, nil)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	created, err := h.videos.CreateVideoRecord(r.Context(), videostate.Record{OwnerKey: fileOwnerKey(identity), Binding: binding, Video: video}, videoOwnerQuota)
	if err != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
		_, _ = runtime.DeleteVideo(ctx, binding, video.ID)
		cancel()
		writeVideoStoreError(w, err)
		return
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
	if h.pipeline.HasModule("billing") {
		writeError(w, http.StatusNotImplemented, "video_billing_unsupported", "video remix requires duration billing support")
		return
	}
	record, ok := h.videoRecord(w, r, owner)
	if !ok {
		return
	}
	video, binding, err := runtime.RemixVideo(r.Context(), identity, record.Binding, record.Video.ID, input, nil)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	created, err := h.videos.CreateVideoRecord(r.Context(), videostate.Record{OwnerKey: owner, Binding: binding, Video: video}, videoOwnerQuota)
	if err != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
		_, _ = runtime.DeleteVideo(ctx, binding, video.ID)
		cancel()
		writeVideoStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, created.Video)
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
