package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

const maxVideoMetadataBytes = 8 << 20
const maxVideoContentBytes = 512 << 20

type VideoListOptions struct {
	After string
	Limit int
	Order string
}

type VideoContent struct {
	Body          io.ReadCloser
	ContentType   string
	ContentLength int64
}

type VideoClient interface {
	CreateVideo(context.Context, openai.VideoCreateRequest) (openai.Video, error)
	ListVideos(context.Context, VideoListOptions) (openai.VideoList, error)
	RetrieveVideo(context.Context, string) (openai.Video, error)
	DeleteVideo(context.Context, string) (openai.VideoDeletion, error)
	DownloadVideoContent(context.Context, string, string) (VideoContent, error)
	RemixVideo(context.Context, string, openai.VideoRemixRequest) (openai.Video, error)
}

func (p OpenAICompatible) CreateVideo(ctx context.Context, input openai.VideoCreateRequest) (openai.Video, error) {
	var result openai.Video
	if param, err := validateVideoCreateRequest(input); err != nil {
		return result, videoParameterError(param, err)
	}
	err := p.videoJSONRequest(ctx, http.MethodPost, "videos", nil, input, &result)
	return result, err
}

func (p OpenAICompatible) ListVideos(ctx context.Context, options VideoListOptions) (openai.VideoList, error) {
	var result openai.VideoList
	query, err := videoListQuery(options)
	if err != nil {
		return result, err
	}
	err = p.videoJSONRequest(ctx, http.MethodGet, "videos", query, nil, &result)
	if err == nil {
		err = validateVideoList(result)
	}
	return result, err
}

func (p OpenAICompatible) RetrieveVideo(ctx context.Context, id string) (openai.Video, error) {
	var result openai.Video
	if !validResponseResourceID(id) {
		return result, videoParameterError("video_id", errors.New("invalid video ID"))
	}
	err := p.videoJSONRequest(ctx, http.MethodGet, "videos/"+id, nil, nil, &result)
	return result, err
}

func (p OpenAICompatible) DeleteVideo(ctx context.Context, id string) (openai.VideoDeletion, error) {
	var result openai.VideoDeletion
	if !validResponseResourceID(id) {
		return result, videoParameterError("video_id", errors.New("invalid video ID"))
	}
	err := p.videoJSONRequest(ctx, http.MethodDelete, "videos/"+id, nil, nil, &result)
	if err == nil && (result.ID != id || result.Object != "video.deleted" || !result.Deleted) {
		err = errors.New("invalid upstream video deletion")
	}
	return result, err
}

func (p OpenAICompatible) RemixVideo(ctx context.Context, id string, input openai.VideoRemixRequest) (openai.Video, error) {
	var result openai.Video
	if !validResponseResourceID(id) {
		return result, videoParameterError("video_id", errors.New("invalid video ID"))
	}
	if len(input.Prompt) == 0 || len(input.Prompt) > 32000 {
		return result, videoParameterError("prompt", errors.New("prompt must contain between 1 and 32000 bytes"))
	}
	err := p.videoJSONRequest(ctx, http.MethodPost, "videos/"+id+"/remix", nil, input, &result)
	return result, err
}

func (p OpenAICompatible) DownloadVideoContent(ctx context.Context, id, variant string) (VideoContent, error) {
	if !validResponseResourceID(id) {
		return VideoContent{}, videoParameterError("video_id", errors.New("invalid video ID"))
	}
	if variant != "" && variant != "video" && variant != "thumbnail" && variant != "spritesheet" {
		return VideoContent{}, videoParameterError("variant", errors.New("invalid video content variant"))
	}
	requestURL := providerURL(p.baseURL, "videos/"+id+"/content")
	if variant != "" {
		requestURL += "?" + url.Values{"variant": []string{variant}}.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, http.NoBody)
	if err != nil {
		return VideoContent{}, err
	}
	request.Header.Set("Accept", "video/*, image/*, application/octet-stream")
	if p.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	client := *p.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return VideoContent{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		return VideoContent{}, responseStatusError(p.providerName(), response)
	}
	if response.ContentLength > maxVideoContentBytes {
		response.Body.Close()
		return VideoContent{}, errors.New("upstream video content exceeds 512 MiB")
	}
	contentType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	if !strings.HasPrefix(contentType, "video/") && !strings.HasPrefix(contentType, "image/") && contentType != "application/octet-stream" {
		response.Body.Close()
		return VideoContent{}, errors.New("invalid upstream video content type")
	}
	return VideoContent{Body: response.Body, ContentType: contentType, ContentLength: response.ContentLength}, nil
}

func (p OpenAICompatible) videoJSONRequest(ctx context.Context, method, path string, query url.Values, input, output any) error {
	var body io.Reader = http.NoBody
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	requestURL := providerURL(p.baseURL, path)
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if p.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	client := *p.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseStatusError(p.providerName(), response)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxVideoMetadataBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > maxVideoMetadataBytes {
		return errors.New("upstream video response exceeds 8 MiB")
	}
	if err := json.Unmarshal(payload, output); err != nil {
		return err
	}
	if video, ok := output.(*openai.Video); ok {
		return validateVideo(*video)
	}
	return nil
}

func videoListQuery(options VideoListOptions) (url.Values, error) {
	if options.After != "" && !validResponseResourceQueryToken(options.After) || options.Limit < 0 || options.Limit > 100 || options.Order != "" && options.Order != "asc" && options.Order != "desc" {
		return nil, errors.New("invalid video pagination")
	}
	query := url.Values{}
	if options.After != "" {
		query.Set("after", options.After)
	}
	if options.Limit > 0 {
		query.Set("limit", strconv.Itoa(options.Limit))
	}
	if options.Order != "" {
		query.Set("order", options.Order)
	}
	return query, nil
}

func validateVideoCreateRequest(input openai.VideoCreateRequest) (string, error) {
	if len(input.Prompt) == 0 || len(input.Prompt) > 32000 {
		return "prompt", errors.New("prompt must contain between 1 and 32000 bytes")
	}
	if len(input.Model) > 256 {
		return "model", errors.New("model exceeds 256 bytes")
	}
	if input.Seconds != "" && input.Seconds != "4" && input.Seconds != "8" && input.Seconds != "12" {
		return "seconds", errors.New("invalid video duration")
	}
	if input.Size != "" && input.Size != "720x1280" && input.Size != "1280x720" && input.Size != "1024x1792" && input.Size != "1792x1024" {
		return "size", errors.New("invalid video size")
	}
	if ref := input.InputReference; ref != nil && ((ref.FileID == "") == (ref.ImageURL == "")) {
		return "input_reference", errors.New("input_reference must contain exactly one source")
	}
	return "", nil
}

func validateVideo(video openai.Video) error {
	if !validResponseResourceID(video.ID) || video.Object != "video" || video.Model == "" || video.Progress < 0 || video.Progress > 100 || video.Seconds == "" || video.Size == "" {
		return errors.New("invalid upstream video")
	}
	switch video.Status {
	case "queued", "in_progress", "completed", "failed":
		return nil
	default:
		return errors.New("invalid upstream video status")
	}
}

func validateVideoList(list openai.VideoList) error {
	if list.Object != "list" || len(list.Data) > 1000 {
		return errors.New("invalid upstream video page")
	}
	for _, video := range list.Data {
		if err := validateVideo(video); err != nil {
			return err
		}
	}
	return nil
}

func videoParameterError(param string, err error) error {
	return &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: param, Err: err}
}
