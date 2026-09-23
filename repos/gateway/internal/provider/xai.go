package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/publichttp"
)

// XAI exposes the provider operations whose wire contracts are validated here.
type XAI struct {
	compatible    OpenAICompatible
	contentClient *http.Client
}

func NewXAI(baseURL, apiKey string, stream bool) XAI {
	compatible := NewOpenAICompatible(baseURL, apiKey, stream)
	compatible.errorProvider = "xai"
	return XAI{compatible: compatible, contentClient: publichttp.NewClient(2 * time.Minute)}
}

func (XAI) SupportsResponses() bool           { return true }
func (XAI) SupportsEmbeddings() bool          { return true }
func (XAI) SupportsTools() bool               { return true }
func (XAI) SupportsStructuredOutput() bool    { return true }
func (XAI) SupportsVision() bool              { return true }
func (XAI) SupportsWebSearch() bool           { return true }
func (XAI) SupportsResponseWebSearch() bool   { return true }
func (XAI) SupportsResponseCustomTools() bool { return true }
func (XAI) SupportsImageGeneration() bool     { return true }
func (XAI) SupportsImageEdit() bool           { return true }
func (XAI) SupportsAudioTranscription() bool  { return true }
func (XAI) SupportsAudioSpeech() bool         { return true }
func (XAI) SupportsVideo() bool               { return true }

func (XAI) ValidateVideoCreateParameters(request openai.VideoCreateRequest) error {
	if param, err := validateVideoCreateRequest(request); err != nil {
		return xaiParameterError(param, err.Error())
	}
	if _, _, err := xaiVideoDimensions(request.Size); err != nil {
		return err
	}
	if request.InputReference != nil {
		if request.InputReference.FileID != "" {
			return xaiUnsupportedParameter("input_reference.file_id")
		}
		if !validXAIMediaURL(request.InputReference.ImageURL) {
			return xaiParameterError("input_reference.image_url", "image URL must use HTTPS")
		}
	}
	return nil
}

func (x XAI) CreateVideo(ctx context.Context, request openai.VideoCreateRequest) (openai.Video, error) {
	if err := x.ValidateVideoCreateParameters(request); err != nil {
		return openai.Video{}, err
	}
	aspectRatio, resolution, _ := xaiVideoDimensions(request.Size)
	size := request.Size
	if size == "" {
		size = "1280x720"
	}
	duration := 4
	if request.Seconds != "" {
		duration, _ = strconv.Atoi(request.Seconds)
	}
	type source struct {
		URL string `json:"url"`
	}
	body := struct {
		Model       string  `json:"model"`
		Prompt      string  `json:"prompt"`
		Duration    int     `json:"duration"`
		AspectRatio string  `json:"aspect_ratio,omitempty"`
		Resolution  string  `json:"resolution,omitempty"`
		Image       *source `json:"image,omitempty"`
	}{Model: request.Model, Prompt: request.Prompt, Duration: duration, AspectRatio: aspectRatio, Resolution: resolution}
	if request.InputReference != nil {
		body.Image = &source{URL: request.InputReference.ImageURL}
	}
	var response struct {
		RequestID string `json:"request_id"`
	}
	if err := x.xaiVideoJSON(ctx, http.MethodPost, "videos/generations", body, &response); err != nil {
		return openai.Video{}, err
	}
	if !validResponseResourceID(response.RequestID) {
		return openai.Video{}, errors.New("invalid xAI video request ID")
	}
	prompt := request.Prompt
	return openai.Video{ID: response.RequestID, Object: "video", Model: request.Model, Status: "queued", Prompt: &prompt, Seconds: strconv.Itoa(duration), Size: size}, nil
}

func (x XAI) RetrieveVideo(ctx context.Context, id string) (openai.Video, error) {
	if !validResponseResourceID(id) {
		return openai.Video{}, xaiParameterError("video_id", "invalid video ID")
	}
	var response struct {
		Status   string  `json:"status"`
		Model    string  `json:"model"`
		Progress float64 `json:"progress"`
		Video    *struct {
			URL      string  `json:"url"`
			Duration float64 `json:"duration"`
		} `json:"video"`
		Usage *struct {
			CostInUSDTicks *int64 `json:"cost_in_usd_ticks"`
		} `json:"usage"`
		Error json.RawMessage `json:"error"`
	}
	if err := x.xaiVideoJSON(ctx, http.MethodGet, "videos/"+id, nil, &response); err != nil {
		return openai.Video{}, err
	}
	status := ""
	switch response.Status {
	case "pending", "queued":
		status = "queued"
	case "in_progress":
		status = "in_progress"
	case "done":
		status = "completed"
	case "failed":
		status = "failed"
	default:
		return openai.Video{}, errors.New("invalid xAI video status")
	}
	result := openai.Video{ID: id, Object: "video", Model: response.Model, Status: status, Progress: response.Progress, Error: response.Error}
	if response.Video != nil {
		result.ContentURL = response.Video.URL
		if response.Video.Duration > 0 && response.Video.Duration <= 15 && response.Video.Duration == math.Trunc(response.Video.Duration) {
			result.Seconds = strconv.Itoa(int(response.Video.Duration))
		}
	}
	if response.Usage != nil {
		result.ProviderCostUSDTicks = response.Usage.CostInUSDTicks
	}
	if status == "completed" && (response.Video == nil || !validXAIMediaURL(result.ContentURL) || result.Seconds == "" || result.ProviderCostUSDTicks == nil || *result.ProviderCostUSDTicks < 0) {
		return openai.Video{}, errors.New("invalid completed xAI video response")
	}
	return result, nil
}

func (x XAI) DownloadVideoContent(ctx context.Context, id, variant string) (VideoContent, error) {
	if variant != "" && variant != "video" {
		return VideoContent{}, xaiUnsupportedParameter("variant")
	}
	video, err := x.RetrieveVideo(ctx, id)
	if err != nil {
		return VideoContent{}, err
	}
	if video.Status != "completed" || !validXAIMediaURL(video.ContentURL) {
		return VideoContent{}, errors.New("xAI video content is not ready")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, video.ContentURL, http.NoBody)
	if err != nil {
		return VideoContent{}, err
	}
	request.Header.Set("Accept", "video/*, application/octet-stream")
	contentClient := x.contentClient
	if contentClient == nil {
		contentClient = publichttp.NewClient(2 * time.Minute)
	}
	response, err := contentClient.Do(request)
	if err != nil {
		return VideoContent{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		return VideoContent{}, responseStatusError("xai", response)
	}
	contentType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	if !strings.HasPrefix(contentType, "video/") && contentType != "application/octet-stream" || response.ContentLength > maxVideoContentBytes {
		response.Body.Close()
		return VideoContent{}, errors.New("invalid xAI video content response")
	}
	return VideoContent{Body: response.Body, ContentType: contentType, ContentLength: response.ContentLength}, nil
}

func (x XAI) RemixVideo(ctx context.Context, id string, request openai.VideoRemixRequest) (openai.Video, error) {
	if !validResponseResourceID(id) {
		return openai.Video{}, xaiParameterError("video_id", "invalid video ID")
	}
	if len(request.Prompt) == 0 || len(request.Prompt) > 32000 {
		return openai.Video{}, xaiParameterError("prompt", "prompt must contain between 1 and 32000 bytes")
	}
	source, err := x.RetrieveVideo(ctx, id)
	if err != nil {
		return openai.Video{}, err
	}
	if source.Status != "completed" || !validXAIMediaURL(source.ContentURL) {
		return openai.Video{}, errors.New("xAI source video is not ready")
	}
	body := struct {
		Prompt string `json:"prompt"`
		Video  struct {
			URL string `json:"url"`
		} `json:"video"`
	}{Prompt: request.Prompt}
	body.Video.URL = source.ContentURL
	var response struct {
		RequestID string `json:"request_id"`
	}
	if err := x.xaiVideoJSON(ctx, http.MethodPost, "videos/edits", body, &response); err != nil {
		return openai.Video{}, err
	}
	if !validResponseResourceID(response.RequestID) {
		return openai.Video{}, errors.New("invalid xAI video edit request ID")
	}
	prompt := request.Prompt
	return openai.Video{ID: response.RequestID, Object: "video", Model: source.Model, Status: "queued", Prompt: &prompt, RemixedFromVideoID: &id, Seconds: source.Seconds}, nil
}

func (x XAI) ExtendVideo(ctx context.Context, id string, request openai.VideoExtendRequest) (openai.Video, error) {
	if !validResponseResourceID(id) {
		return openai.Video{}, xaiParameterError("video_id", "invalid video ID")
	}
	if err := x.ValidateVideoExtendParameters(request); err != nil {
		return openai.Video{}, err
	}
	duration := 4
	if request.Seconds != "" {
		duration, _ = strconv.Atoi(request.Seconds)
	}
	source, err := x.RetrieveVideo(ctx, id)
	if err != nil {
		return openai.Video{}, err
	}
	if source.Status != "completed" || !validXAIMediaURL(source.ContentURL) {
		return openai.Video{}, errors.New("xAI source video is not ready")
	}
	body := struct {
		Prompt   string `json:"prompt"`
		Duration int    `json:"duration"`
		Video    struct {
			URL string `json:"url"`
		} `json:"video"`
	}{Prompt: request.Prompt, Duration: duration}
	body.Video.URL = source.ContentURL
	var response struct {
		RequestID string `json:"request_id"`
	}
	if err := x.xaiVideoJSON(ctx, http.MethodPost, "videos/extensions", body, &response); err != nil {
		return openai.Video{}, err
	}
	if !validResponseResourceID(response.RequestID) {
		return openai.Video{}, errors.New("invalid xAI video extension request ID")
	}
	prompt := request.Prompt
	return openai.Video{ID: response.RequestID, Object: "video", Model: source.Model, Status: "queued", Prompt: &prompt, RemixedFromVideoID: &id, Seconds: strconv.Itoa(duration)}, nil
}

func (XAI) ValidateVideoExtendParameters(request openai.VideoExtendRequest) error {
	if len(request.Prompt) == 0 || len(request.Prompt) > 32000 {
		return xaiParameterError("prompt", "prompt must contain between 1 and 32000 bytes")
	}
	if request.Seconds != "" {
		duration, err := strconv.Atoi(request.Seconds)
		if err != nil || duration != 4 && duration != 8 && duration != 12 {
			return xaiParameterError("seconds", "seconds must be 4, 8, or 12")
		}
	}
	return nil
}

func (XAI) ListVideos(context.Context, VideoListOptions) (openai.VideoList, error) {
	return openai.VideoList{}, xaiUnsupportedParameter("list")
}

func (XAI) DeleteVideo(_ context.Context, id string) (openai.VideoDeletion, error) {
	if !validResponseResourceID(id) {
		return openai.VideoDeletion{}, xaiParameterError("video_id", "invalid video ID")
	}
	return openai.VideoDeletion{ID: id, Object: "video.deleted", Deleted: true}, nil
}

func (x XAI) xaiVideoJSON(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader = http.NoBody
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, providerURL(x.compatible.baseURL, path), body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if x.compatible.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+x.compatible.apiKey)
	}
	response, err := x.compatible.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseStatusError("xai", response)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxVideoMetadataBytes+1))
	if err != nil || len(payload) > maxVideoMetadataBytes || json.Unmarshal(payload, output) != nil {
		return errors.New("invalid xAI video response")
	}
	return nil
}

func xaiVideoDimensions(size string) (string, string, error) {
	switch size {
	case "":
		return "16:9", "720p", nil
	case "720x1280":
		return "9:16", "720p", nil
	case "1280x720":
		return "16:9", "720p", nil
	default:
		return "", "", xaiParameterError("size", "xAI video size must be 720x1280 or 1280x720")
	}
}

func validXAIMediaURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

func (x XAI) ReserveAudioMilliseconds(request openai.AudioTranscriptionRequest) (int, error) {
	return x.compatible.ReserveTranslationAudioMilliseconds(request)
}

func (x XAI) ValidateAudioTranscriptionParameters(request openai.AudioTranscriptionRequest) error {
	if message := request.Validate(); message != "" {
		return xaiParameterError("", message)
	}
	for _, keyword := range request.Keywords {
		if len([]rune(keyword)) > 50 {
			return xaiParameterError("keywords", "each keyword must be at most 50 characters")
		}
	}
	if err := rejectParameters("xai",
		parameterCheck{"prompt", request.Prompt != ""},
		parameterCheck{"timestamp_granularities", len(request.TimestampGranularities) > 0},
		parameterCheck{"response_format", request.ResponseFormat != ""},
		parameterCheck{"temperature", request.Temperature != nil},
		parameterCheck{"include", len(request.Include) > 0},
		parameterCheck{"languages", len(request.Languages) > 0},
		parameterCheck{"mode", request.Mode != ""},
		parameterCheck{"chunking_strategy", request.ChunkingStrategy != nil},
		parameterCheck{"known_speaker_names", len(request.KnownSpeakerNames) > 0},
		parameterCheck{"known_speaker_references", len(request.KnownSpeakerReferences) > 0},
		parameterCheck{"stream", request.Stream},
	); err != nil {
		return err
	}
	return nil
}

func (x XAI) TranscribeAudio(ctx context.Context, request openai.AudioTranscriptionRequest) (openai.AudioTranscriptionResponse, error) {
	if err := x.ValidateAudioTranscriptionParameters(request); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	if _, err := x.ReserveAudioMilliseconds(request); err != nil {
		return openai.AudioTranscriptionResponse{}, xaiParameterError("file", err.Error())
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if request.Language != "" {
		if err := writer.WriteField("language", request.Language); err != nil {
			return openai.AudioTranscriptionResponse{}, err
		}
	}
	for _, keyword := range request.Keywords {
		if err := writer.WriteField("keyterm", keyword); err != nil {
			return openai.AudioTranscriptionResponse{}, err
		}
	}
	if err := writeAudioPart(writer, request.File); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	if err := writer.Close(); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(x.compatible.baseURL, "stt"), &body)
	if err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", writer.FormDataContentType())
	if x.compatible.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+x.compatible.apiKey)
	}
	response, err := x.compatible.client.Do(httpRequest)
	if err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.AudioTranscriptionResponse{}, responseStatusError("xai", response)
	}
	return decodeXAITranscriptionResponse(response.Body)
}

func decodeXAITranscriptionResponse(reader io.Reader) (openai.AudioTranscriptionResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxAudioTranscriptionResponseBytes+1))
	if err != nil || len(payload) > maxAudioTranscriptionResponseBytes {
		return openai.AudioTranscriptionResponse{}, errors.New("audio transcription response exceeds limit")
	}
	var wire *struct {
		Text     string  `json:"text"`
		Language string  `json:"language"`
		Duration float64 `json:"duration"`
		Words    []struct {
			Text  string  `json:"text"`
			Start float64 `json:"start"`
			End   float64 `json:"end"`
		} `json:"words"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil || wire == nil {
		return openai.AudioTranscriptionResponse{}, errors.New("audio transcription response must be an object")
	}
	if wire.Duration <= 0 || math.IsNaN(wire.Duration) || math.IsInf(wire.Duration, 0) || wire.Duration > 7*24*60*60 {
		return openai.AudioTranscriptionResponse{}, errors.New("invalid transcription duration")
	}
	durationMilliseconds := int(math.Ceil(wire.Duration * 1000))
	result := openai.AudioTranscriptionResponse{
		Text: wire.Text, Language: wire.Language, Duration: wire.Duration,
		Usage: &openai.AudioTranscriptionUsage{Type: "duration", InputAudioMilliseconds: durationMilliseconds},
		Words: make([]openai.AudioTranscriptionWord, len(wire.Words)),
	}
	for index, word := range wire.Words {
		result.Words[index] = openai.AudioTranscriptionWord{Word: word.Text, Start: word.Start, End: word.End}
	}
	if message := result.Validate(); message != "" {
		return openai.AudioTranscriptionResponse{}, errors.New(message)
	}
	return result, nil
}

func (XAI) ValidateAudioSpeechParameters(request openai.AudioSpeechRequest) error {
	if message := request.Validate(); message != "" {
		return xaiParameterError("", message)
	}
	if request.Speed != nil && (*request.Speed < 0.7 || *request.Speed > 1.5) {
		return xaiParameterError("speed", "speed must be between 0.7 and 1.5")
	}
	if request.ResponseFormat != "" && request.ResponseFormat != "mp3" && request.ResponseFormat != "wav" && request.ResponseFormat != "pcm" {
		return xaiParameterError("response_format", "response_format must be mp3, wav, or pcm")
	}
	return rejectParameters("xai", parameterCheck{"instructions", request.Instructions != ""}, parameterCheck{"stream_format", request.StreamFormat == "sse"})
}

func (x XAI) GenerateSpeech(ctx context.Context, request openai.AudioSpeechRequest) (openai.AudioSpeechResponse, error) {
	if err := x.ValidateAudioSpeechParameters(request); err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	language := request.Language
	if language == "" {
		language = "auto"
	}
	type outputFormat struct {
		Codec string `json:"codec"`
	}
	body := struct {
		Text         string        `json:"text"`
		VoiceID      string        `json:"voice_id"`
		Language     string        `json:"language"`
		OutputFormat *outputFormat `json:"output_format,omitempty"`
		Speed        *float64      `json:"speed,omitempty"`
	}{Text: request.Input, VoiceID: request.Voice, Language: language, Speed: request.Speed}
	if request.ResponseFormat != "" {
		body.OutputFormat = &outputFormat{Codec: request.ResponseFormat}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(x.compatible.baseURL, "tts"), bytes.NewReader(payload))
	if err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if x.compatible.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+x.compatible.apiKey)
	}
	response, err := x.compatible.client.Do(httpRequest)
	if err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.AudioSpeechResponse{}, responseStatusError("xai", response)
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(response.Header.Get("Content-Type")))
	if err != nil || mediaType != request.ExpectedContentType() {
		return openai.AudioSpeechResponse{}, errors.New("audio speech response has an unsupported content type")
	}
	data, err := readAudioSpeechResponse(response.Body)
	if err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	return openai.AudioSpeechResponse{Data: data, ContentType: mediaType, Model: request.Model}, nil
}

func (x XAI) GenerateImage(ctx context.Context, request openai.ImageGenerationRequest) (openai.ImageGenerationResponse, error) {
	if err := x.ValidateImageGenerationParameters(request); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	body, err := json.Marshal(struct {
		Model          string `json:"model"`
		Prompt         string `json:"prompt"`
		N              *int   `json:"n,omitempty"`
		Quality        string `json:"quality,omitempty"`
		ResponseFormat string `json:"response_format,omitempty"`
		Resolution     string `json:"resolution,omitempty"`
		AspectRatio    string `json:"aspect_ratio,omitempty"`
	}{request.Model, request.Prompt, request.N, request.Quality, request.ResponseFormat, strings.ToLower(request.Resolution), request.AspectRatio})
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	return x.compatible.postImageJSON(ctx, "images/generations", body, request, true)
}

func (XAI) ValidateImageGenerationParameters(request openai.ImageGenerationRequest) error {
	if message := request.Validate(); message != "" {
		return xaiParameterError("", message)
	}
	if request.Quality != "" && request.Quality != "auto" && request.Quality != "low" && request.Quality != "medium" {
		return xaiParameterError("quality", "quality must be auto, low, or medium")
	}
	if request.Resolution != "" && request.Resolution != "1K" && request.Resolution != "2K" {
		return xaiParameterError("resolution", "resolution must be 1K or 2K")
	}
	if err := rejectParameters("xai",
		parameterCheck{"size", request.Size != ""},
		parameterCheck{"style", request.Style != ""},
		parameterCheck{"user", request.User != ""},
		parameterCheck{"background", request.Background != ""},
		parameterCheck{"output_format", request.OutputFormat != ""},
		parameterCheck{"output_compression", request.OutputCompression != nil},
		parameterCheck{"seed", request.Seed != nil},
		parameterCheck{"stream", request.Stream},
		parameterCheck{"partial_images", request.PartialImages != nil},
	); err != nil {
		return err
	}
	return nil
}

func (x XAI) EditImage(ctx context.Context, request openai.ImageEditRequest) (openai.ImageGenerationResponse, error) {
	if err := x.ValidateImageEditParameters(request); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	type source struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	sources := make([]source, len(request.Images))
	for index, image := range request.Images {
		sources[index] = source{Type: "image_url", URL: "data:" + image.MediaType + ";base64," + image.Data}
	}
	body := struct {
		Model          string   `json:"model"`
		Prompt         string   `json:"prompt"`
		Image          *source  `json:"image,omitempty"`
		Images         []source `json:"images,omitempty"`
		N              *int     `json:"n,omitempty"`
		Quality        string   `json:"quality,omitempty"`
		ResponseFormat string   `json:"response_format,omitempty"`
	}{Model: request.Model, Prompt: request.Prompt, N: request.N, Quality: request.Quality, ResponseFormat: request.ResponseFormat}
	if len(sources) == 1 {
		body.Image = &sources[0]
	} else {
		body.Images = sources
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	return x.compatible.postImageJSON(ctx, "images/edits", payload, request.GenerationRequest(), true)
}

func (XAI) ValidateImageEditParameters(request openai.ImageEditRequest) error {
	if message := request.Validate(); message != "" {
		return xaiParameterError("", message)
	}
	if len(request.Images) > 5 {
		return xaiParameterError("images", "xAI image edits accept at most five source images")
	}
	if request.Quality != "" && request.Quality != "auto" && request.Quality != "low" && request.Quality != "medium" {
		return xaiParameterError("quality", "quality must be auto, low, or medium")
	}
	if err := rejectParameters("xai",
		parameterCheck{"mask", request.Mask != nil},
		parameterCheck{"size", request.Size != ""},
		parameterCheck{"user", request.User != ""},
		parameterCheck{"background", request.Background != ""},
		parameterCheck{"output_format", request.OutputFormat != ""},
		parameterCheck{"output_compression", request.OutputCompression != nil},
		parameterCheck{"stream", request.Stream},
		parameterCheck{"partial_images", request.PartialImages != nil},
	); err != nil {
		return err
	}
	return nil
}

func (x XAI) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if !validXAIServiceTier(request.ServiceTier) {
		return xaiParameterError("service_tier", "service_tier must be default or priority")
	}
	if !validXAIReasoningEffort(request.ReasoningEffort) {
		return xaiParameterError("reasoning_effort", "reasoning_effort must be none, low, medium, high, or xhigh")
	}
	if request.TopLogprobs != nil && (*request.TopLogprobs < 0 || *request.TopLogprobs > 8 || request.Logprobs == nil || !*request.Logprobs) {
		return xaiParameterError("top_logprobs", "top_logprobs must be between 0 and 8 and requires logprobs=true")
	}
	if err := rejectParameters("xai",
		parameterCheck{"metadata", request.Metadata != nil},
		parameterCheck{"store", request.Store != nil},
		parameterCheck{"modalities", request.Modalities != nil},
		parameterCheck{"audio", request.Audio != nil},
		parameterCheck{"safe_prompt", request.SafePrompt != nil},
		parameterCheck{"safety_identifier", request.SafetyIdentifier != ""},
		parameterCheck{"prompt_cache_options", request.PromptCacheOptions != nil},
		parameterCheck{"prompt_cache_retention", request.PromptCacheRetention != ""},
		parameterCheck{"prompt_mode", request.PromptMode != ""},
		parameterCheck{"prediction", request.Prediction != nil},
		parameterCheck{"verbosity", request.Verbosity != ""},
		parameterCheck{"web_fetch_options", request.WebFetchOptions != nil},
		parameterCheck{"min_p", request.MinP != nil},
		parameterCheck{"top_k", request.TopK != nil},
		parameterCheck{"top_a", request.TopA != nil},
		parameterCheck{"repetition_penalty", request.RepetitionPenalty != nil},
		parameterCheck{"logit_bias", request.LogitBias != nil},
	); err != nil {
		return err
	}
	return x.compatible.ValidateChatParameters(request)
}

func (x XAI) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if err := x.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return x.compatible.ChatCompletions(ctx, request)
}

func (x XAI) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if err := x.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return x.compatible.StreamChatCompletions(ctx, request, write)
}

func (x XAI) ValidateResponseParameters(request openai.ResponseRequest) error {
	if request.Background {
		return xaiUnsupportedParameter("background")
	}
	if len(request.ContextManagement) > 0 {
		return xaiUnsupportedParameter("context_management")
	}
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "xai", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	if !validXAIServiceTier(request.ServiceTier) {
		return xaiParameterError("service_tier", "service_tier must be default or priority")
	}
	if request.TopLogprobs != nil {
		return xaiUnsupportedParameter("top_logprobs")
	}
	if request.FrequencyPenalty != nil {
		return xaiUnsupportedParameter("frequency_penalty")
	}
	if request.PresencePenalty != nil {
		return xaiUnsupportedParameter("presence_penalty")
	}
	if len(request.Metadata) > 0 {
		return xaiUnsupportedParameter("metadata")
	}
	if request.Truncation != nil {
		return xaiUnsupportedParameter("truncation")
	}
	if request.SafetyIdentifier != "" {
		return xaiUnsupportedParameter("safety_identifier")
	}
	if request.PromptCacheOptions != nil {
		return xaiUnsupportedParameter("prompt_cache_options")
	}
	if request.PromptCacheRetention != "" {
		return xaiUnsupportedParameter("prompt_cache_retention")
	}
	if request.StreamOptions != nil {
		return xaiUnsupportedParameter("stream_options")
	}
	if request.MaxToolCalls != nil {
		return xaiUnsupportedParameter("max_tool_calls")
	}
	if _, supplied := openai.ResponseTextVerbosity(request.Text); supplied {
		return xaiUnsupportedParameter("text.verbosity")
	}
	if reasoning := request.Reasoning; reasoning != nil {
		if reasoning.Summary != nil || reasoning.GenerateSummary != nil || reasoning.Context != nil || reasoning.Mode != nil {
			return xaiUnsupportedParameter("reasoning")
		}
		if reasoning.Effort != nil && !validXAIReasoningEffort(*reasoning.Effort) {
			return xaiParameterError("reasoning.effort", "reasoning effort must be low, medium, high, or xhigh")
		}
	}
	return x.compatible.ValidateResponseParameters(request)
}

func (x XAI) Responses(ctx context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	if err := x.ValidateResponseParameters(request); err != nil {
		return openai.ResponseResponse{}, err
	}
	return x.compatible.Responses(ctx, request)
}

func (x XAI) StreamResponses(ctx context.Context, request openai.ResponseRequest, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	if err := x.ValidateResponseParameters(request); err != nil {
		return openai.ResponseResponse{}, err
	}
	return x.compatible.StreamResponses(ctx, request, write)
}

func (x XAI) ValidateEmbeddingParameters(request openai.EmbeddingRequest) error {
	return rejectParameters("xai",
		parameterCheck{"metadata", request.Metadata != nil},
		parameterCheck{"input_type", request.InputType != ""},
		parameterCheck{"output_dtype", request.OutputDType != ""},
	)
}

func (x XAI) Embeddings(ctx context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	if err := x.ValidateEmbeddingParameters(request); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	return x.compatible.Embeddings(ctx, request)
}

func (x XAI) RetrieveResponse(ctx context.Context, id string) (openai.ResponseResponse, error) {
	return x.compatible.RetrieveResponse(ctx, id)
}

func (x XAI) ListResponseInputItems(ctx context.Context, id string, options ResponseInputItemsOptions) (openai.ResponseInputItemList, error) {
	return x.compatible.ListResponseInputItems(ctx, id, options)
}

func (x XAI) DeleteResponse(ctx context.Context, id string) (openai.ResponseDeletion, error) {
	return x.compatible.deleteResponse(ctx, id, "response")
}

func (x XAI) CompactResponse(ctx context.Context, request openai.ResponseCompactRequest) (openai.CompactedResponse, error) {
	return x.compatible.CompactResponse(ctx, request)
}

func validXAIServiceTier(value string) bool {
	return value == "" || value == "default" || value == "priority"
}

func validXAIReasoningEffort(value string) bool {
	return value == "" || value == "none" || value == "low" || value == "medium" || value == "high" || value == "xhigh"
}

func xaiParameterError(param, message string) error {
	return &Error{Class: FailureClientRequest, Provider: "xai", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: param, Err: errors.New(message)}
}

func xaiUnsupportedParameter(param string) error {
	return &Error{Class: FailureClientRequest, Provider: "xai", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: param, Err: errors.New(param + " is not supported by this adapter")}
}
