package openai

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const MaxAudioBytes = 20 << 20

const maxAudioTranscriptRunes = 1 << 20

const (
	MaxAudioLanguages             = 64
	MaxAudioKeywords              = 100
	MaxKnownSpeakerReferences     = 4
	MaxKnownSpeakerReferenceBytes = 512 << 10
	MaxKnownSpeakerTotalBytes     = 2 << 20
)

var audioLanguagePattern = regexp.MustCompile(`^[A-Za-z]{2,3}(?:-[A-Za-z]{2,8})?$`)

type AudioAttachment struct {
	Filename  string `json:"filename"`
	MediaType string `json:"media_type"`
	Data      string `json:"data_base64"`
}

type AudioTranscriptionRequest struct {
	Provider               string                 `json:"provider,omitempty"`
	Model                  string                 `json:"model"`
	File                   AudioAttachment        `json:"file"`
	Language               string                 `json:"language,omitempty"`
	Prompt                 string                 `json:"prompt,omitempty"`
	ResponseFormat         string                 `json:"response_format,omitempty"`
	Temperature            *float64               `json:"temperature,omitempty"`
	TimestampGranularities []string               `json:"timestamp_granularities,omitempty"`
	Include                []string               `json:"include,omitempty"`
	Languages              []string               `json:"languages,omitempty"`
	Keywords               []string               `json:"keywords,omitempty"`
	ChunkingStrategy       *AudioChunkingStrategy `json:"chunking_strategy,omitempty"`
	KnownSpeakerNames      []string               `json:"known_speaker_names,omitempty"`
	KnownSpeakerReferences []AudioAttachment      `json:"known_speaker_references,omitempty"`
}

type AudioChunkingStrategy struct {
	Type              string   `json:"type"`
	PrefixPaddingMS   *int     `json:"prefix_padding_ms,omitempty"`
	SilenceDurationMS *int     `json:"silence_duration_ms,omitempty"`
	Threshold         *float64 `json:"threshold,omitempty"`
}

func ParseAudioChunkingStrategy(value string) (*AudioChunkingStrategy, error) {
	if value == "auto" {
		return &AudioChunkingStrategy{Type: "auto"}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	var strategy *AudioChunkingStrategy
	if err := decoder.Decode(&strategy); err != nil || strategy == nil {
		return nil, errors.New("invalid chunking_strategy")
	}
	if err := ensureJSONEOF(decoder); err != nil || strategy.validate() != "" {
		return nil, errors.New("invalid chunking_strategy")
	}
	return strategy, nil
}

func (s AudioChunkingStrategy) validate() string {
	if s.Type == "auto" {
		if s.PrefixPaddingMS != nil || s.SilenceDurationMS != nil || s.Threshold != nil {
			return "auto chunking_strategy cannot have server_vad settings"
		}
		return ""
	}
	if s.Type != "server_vad" {
		return "unsupported chunking_strategy type"
	}
	if s.PrefixPaddingMS != nil && (*s.PrefixPaddingMS < 0 || *s.PrefixPaddingMS > 10000) {
		return "prefix_padding_ms must be between 0 and 10000"
	}
	if s.SilenceDurationMS != nil && (*s.SilenceDurationMS < 0 || *s.SilenceDurationMS > 10000) {
		return "silence_duration_ms must be between 0 and 10000"
	}
	if s.Threshold != nil && (math.IsNaN(*s.Threshold) || math.IsInf(*s.Threshold, 0) || *s.Threshold < 0 || *s.Threshold > 1) {
		return "threshold must be between 0 and 1"
	}
	return ""
}

func (s AudioChunkingStrategy) MultipartValue() (string, error) {
	if message := s.validate(); message != "" {
		return "", errors.New(message)
	}
	if s.Type == "auto" {
		return "auto", nil
	}
	payload, err := json.Marshal(s)
	return string(payload), err
}

func (r AudioTranscriptionRequest) Validate() string {
	if strings.TrimSpace(r.Model) == "" {
		return "model is required"
	}
	if err := ValidateAudioAttachment(r.File); err != nil {
		return err.Error()
	}
	if utf8.RuneCountInString(r.Language) > 32 || utf8.RuneCountInString(r.Prompt) > 32000 {
		return "language or prompt exceeds its limit"
	}
	if !oneOfOrEmpty(r.ResponseFormat, "json", "verbose_json", "diarized_json") {
		return "response_format must preserve token usage"
	}
	if r.Temperature != nil && (math.IsNaN(*r.Temperature) || math.IsInf(*r.Temperature, 0) || *r.Temperature < 0 || *r.Temperature > 1) {
		return "temperature must be between 0 and 1"
	}
	if len(r.TimestampGranularities) > 2 || !uniqueAllowed(r.TimestampGranularities, "word", "segment") {
		return "unsupported timestamp_granularities value"
	}
	if len(r.TimestampGranularities) > 0 && r.ResponseFormat != "verbose_json" {
		return "timestamp_granularities requires verbose_json"
	}
	if len(r.Include) > 1 || !uniqueAllowed(r.Include, "logprobs") {
		return "unsupported include value"
	}
	if len(r.Include) > 0 && r.ResponseFormat != "" && r.ResponseFormat != "json" {
		return "include logprobs requires json"
	}
	if len(r.Languages) > MaxAudioLanguages {
		return "languages exceeds its limit"
	}
	for _, language := range r.Languages {
		if !audioLanguagePattern.MatchString(language) {
			return "invalid languages value"
		}
	}
	if len(r.Keywords) > MaxAudioKeywords {
		return "keywords exceeds its limit"
	}
	keywordRunes := 0
	for _, keyword := range r.Keywords {
		count := utf8.RuneCountInString(keyword)
		if strings.TrimSpace(keyword) == "" || count > 128 {
			return "invalid keywords value"
		}
		keywordRunes += count
	}
	if keywordRunes > 4096 {
		return "keywords exceeds its total limit"
	}
	if r.ChunkingStrategy != nil && r.ChunkingStrategy.validate() != "" {
		return r.ChunkingStrategy.validate()
	}
	if len(r.KnownSpeakerNames) != len(r.KnownSpeakerReferences) || len(r.KnownSpeakerNames) > MaxKnownSpeakerReferences {
		return "known speaker names and references must have the same length up to 4"
	}
	for _, name := range r.KnownSpeakerNames {
		if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 64 {
			return "invalid known speaker name"
		}
	}
	if err := ValidateKnownSpeakerReferences(r.KnownSpeakerReferences); err != nil {
		return err.Error()
	}
	return ""
}

func ParseDataAudioURL(value string) (AudioAttachment, error) {
	header, data, found := strings.Cut(value, ",")
	if !found || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") {
		return AudioAttachment{}, fmt.Errorf("%w: only base64 data audio URLs are supported", ErrInvalidAudio)
	}
	mediaType := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
	extension := audioExtension(mediaType)
	if extension == "" || data == "" || base64.StdEncoding.DecodedLen(len(data)) > MaxKnownSpeakerReferenceBytes {
		return AudioAttachment{}, ErrInvalidAudio
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(decoded) == 0 || len(decoded) > MaxKnownSpeakerReferenceBytes || !validAudioSignature(mediaType, decoded) {
		return AudioAttachment{}, ErrInvalidAudio
	}
	return AudioAttachment{Filename: "reference" + extension, MediaType: mediaType, Data: data}, nil
}

func (a AudioAttachment) DataURL() string {
	return "data:" + a.MediaType + ";base64," + a.Data
}

func ValidateKnownSpeakerReferences(references []AudioAttachment) error {
	if len(references) > MaxKnownSpeakerReferences {
		return ErrInvalidAudio
	}
	total := 0
	for _, reference := range references {
		parsed, err := ParseDataAudioURL(reference.DataURL())
		if err != nil || parsed.MediaType != reference.MediaType {
			return ErrInvalidAudio
		}
		decoded, err := base64.StdEncoding.DecodeString(reference.Data)
		if err != nil || total > MaxKnownSpeakerTotalBytes-len(decoded) {
			return ErrInvalidAudio
		}
		total += len(decoded)
	}
	return nil
}

func ValidateAudioAttachment(attachment AudioAttachment) error {
	if attachment.Filename == "" || filepath.Base(attachment.Filename) != attachment.Filename || len(attachment.Filename) > 256 {
		return ErrInvalidAudio
	}
	data, err := base64.StdEncoding.DecodeString(attachment.Data)
	if err != nil || len(data) == 0 || len(data) > MaxAudioBytes || !validAudioSignature(attachment.MediaType, data) {
		return ErrInvalidAudio
	}
	return nil
}

func AudioTranscriptionInputTokens(r AudioTranscriptionRequest) int {
	data, err := base64.StdEncoding.DecodeString(r.File.Data)
	if err != nil {
		return int(^uint(0) >> 1)
	}
	contextParts := make([]string, 0, 1+len(r.Languages)+len(r.Keywords)+len(r.KnownSpeakerNames))
	contextParts = append(contextParts, r.Prompt)
	contextParts = append(contextParts, r.Languages...)
	contextParts = append(contextParts, r.Keywords...)
	contextParts = append(contextParts, r.KnownSpeakerNames...)
	if r.ChunkingStrategy != nil {
		if value, marshalErr := r.ChunkingStrategy.MultipartValue(); marshalErr == nil {
			contextParts = append(contextParts, value)
		} else {
			return int(^uint(0) >> 1)
		}
	}
	promptTokens := EstimateContextTokens(strings.Join(contextParts, "\n"))
	audioTokens := bytesTokenEstimate(data)
	for _, reference := range r.KnownSpeakerReferences {
		decoded, decodeErr := base64.StdEncoding.DecodeString(reference.Data)
		if decodeErr != nil {
			return int(^uint(0) >> 1)
		}
		referenceTokens := bytesTokenEstimate(decoded)
		maxInt := int(^uint(0) >> 1)
		if audioTokens > maxInt-referenceTokens {
			return maxInt
		}
		audioTokens += referenceTokens
	}
	maxInt := int(^uint(0) >> 1)
	if promptTokens > maxInt-audioTokens {
		return maxInt
	}
	return promptTokens + audioTokens
}

func audioExtension(mediaType string) string {
	switch strings.ToLower(mediaType) {
	case "audio/wav", "audio/wave", "audio/x-wav":
		return ".wav"
	case "audio/flac":
		return ".flac"
	case "audio/ogg":
		return ".ogg"
	case "audio/webm", "video/webm":
		return ".webm"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/mp4", "video/mp4", "audio/x-m4a":
		return ".m4a"
	default:
		return ""
	}
}

func AudioTranscriptionReserveTokens(r AudioTranscriptionRequest) int {
	return ReserveTokens(AudioTranscriptionInputTokens(r), DefaultOutputTokenReserve)
}

func bytesTokenEstimate(data []byte) int {
	tokens := len(data) / 4
	if len(data)%4 != 0 {
		tokens++
	}
	return tokens
}

func validAudioSignature(mediaType string, data []byte) bool {
	switch strings.ToLower(mediaType) {
	case "audio/wav", "audio/wave", "audio/x-wav":
		return len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE"
	case "audio/flac":
		return len(data) >= 4 && string(data[:4]) == "fLaC"
	case "audio/ogg":
		return len(data) >= 4 && string(data[:4]) == "OggS"
	case "audio/webm", "video/webm":
		return len(data) >= 4 && data[0] == 0x1a && data[1] == 0x45 && data[2] == 0xdf && data[3] == 0xa3
	case "audio/mpeg", "audio/mp3":
		return len(data) >= 3 && (string(data[:3]) == "ID3" || (data[0] == 0xff && data[1]&0xe0 == 0xe0))
	case "audio/mp4", "video/mp4", "audio/x-m4a":
		return len(data) >= 12 && string(data[4:8]) == "ftyp"
	default:
		return false
	}
}

func uniqueAllowed(values []string, allowed ...string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] || !oneOfOrEmpty(value, allowed...) || value == "" {
			return false
		}
		seen[value] = true
	}
	return true
}

var ErrInvalidAudio = errors.New("invalid or unsupported audio file")

type AudioTranscriptionWord struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type AudioTranscriptionSegment struct {
	ID               any     `json:"id"`
	Type             string  `json:"type,omitempty"`
	Speaker          string  `json:"speaker,omitempty"`
	Text             string  `json:"text"`
	Start            float64 `json:"start"`
	End              float64 `json:"end"`
	Seek             int     `json:"seek,omitempty"`
	Tokens           []int   `json:"tokens,omitempty"`
	Temperature      float64 `json:"temperature,omitempty"`
	AverageLogprob   float64 `json:"avg_logprob,omitempty"`
	CompressionRatio float64 `json:"compression_ratio,omitempty"`
	NoSpeechProb     float64 `json:"no_speech_prob,omitempty"`
}

type AudioTranscriptionLanguage struct {
	Code string `json:"code"`
}

type AudioTranscriptionLogprob struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
	Bytes   []int   `json:"bytes,omitempty"`
}

type AudioTranscriptionResponse struct {
	Text      string                       `json:"text"`
	Task      string                       `json:"task,omitempty"`
	Language  string                       `json:"language,omitempty"`
	Languages []AudioTranscriptionLanguage `json:"languages,omitempty"`
	Duration  float64                      `json:"duration,omitempty"`
	Words     []AudioTranscriptionWord     `json:"words,omitempty"`
	Segments  []AudioTranscriptionSegment  `json:"segments,omitempty"`
	Logprobs  []AudioTranscriptionLogprob  `json:"logprobs,omitempty"`
	Usage     *AudioTranscriptionUsage     `json:"usage,omitempty"`
}

func (r AudioTranscriptionResponse) Validate() string {
	if utf8.RuneCountInString(r.Text) > maxAudioTranscriptRunes || r.Text == "" {
		return "transcription text is empty or too large"
	}
	if utf8.RuneCountInString(r.Task) > 64 || utf8.RuneCountInString(r.Language) > 64 || len(r.Languages) > 256 || !finiteNonNegative(r.Duration) || len(r.Words)+len(r.Segments) > 100000 || len(r.Logprobs) > 100000 {
		return "invalid transcription timing data"
	}
	for _, language := range r.Languages {
		if language.Code == "" || utf8.RuneCountInString(language.Code) > 32 {
			return "invalid transcription language"
		}
	}
	for _, word := range r.Words {
		if word.Word == "" || utf8.RuneCountInString(word.Word) > 4096 || !validTimeRange(word.Start, word.End) {
			return "invalid transcription word"
		}
	}
	for _, segment := range r.Segments {
		if !validAudioSegmentID(segment.ID) || utf8.RuneCountInString(segment.Type) > 64 || utf8.RuneCountInString(segment.Speaker) > 256 || segment.Text == "" || utf8.RuneCountInString(segment.Text) > 65536 || !validTimeRange(segment.Start, segment.End) || len(segment.Tokens) > 100000 || segment.Seek < 0 || !finiteNumber(segment.Temperature) || !finiteNumber(segment.AverageLogprob) || !finiteNumber(segment.CompressionRatio) || !finiteNumber(segment.NoSpeechProb) {
			return "invalid transcription segment"
		}
		for _, token := range segment.Tokens {
			if token < 0 {
				return "invalid transcription segment token"
			}
		}
	}
	for _, logprob := range r.Logprobs {
		if utf8.RuneCountInString(logprob.Token) > 4096 || math.IsNaN(logprob.Logprob) || math.IsInf(logprob.Logprob, 0) || len(logprob.Bytes) > 4096 {
			return "invalid transcription logprob"
		}
		for _, value := range logprob.Bytes {
			if value < 0 || value > 255 {
				return "invalid transcription logprob bytes"
			}
		}
	}
	if r.Usage == nil || (r.Usage.Type != "" && r.Usage.Type != "tokens") || !exactPositiveTokenSum(r.Usage.InputTokens, r.Usage.OutputTokens, r.Usage.TotalTokens) {
		return "exact token usage is required"
	}
	if details := r.Usage.InputTokenDetails; details != nil {
		if details.TextTokens < 0 || details.AudioTokens < 0 || details.TextTokens > r.Usage.InputTokens || details.AudioTokens > r.Usage.InputTokens-details.TextTokens {
			return "invalid transcription input token details"
		}
	}
	return ""
}

func exactPositiveTokenSum(first, second, total int) bool {
	return total > 0 && exactNonNegativeTokenSum(first, second, total)
}

func exactNonNegativeTokenSum(first, second, total int) bool {
	return first >= 0 && second >= 0 && total >= 0 && first <= total && second == total-first
}

func finiteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func finiteNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validAudioSegmentID(value any) bool {
	switch typed := value.(type) {
	case int:
		return typed >= 0
	case float64:
		return finiteNonNegative(typed) && typed == math.Trunc(typed)
	case string:
		return typed != "" && utf8.RuneCountInString(typed) <= 256
	default:
		return false
	}
}

func validTimeRange(start, end float64) bool {
	return finiteNonNegative(start) && finiteNonNegative(end) && end >= start
}

type AudioTranscriptionUsage struct {
	Type              string                               `json:"type,omitempty"`
	InputTokens       int                                  `json:"input_tokens"`
	InputTokenDetails *AudioTranscriptionInputTokenDetails `json:"input_token_details,omitempty"`
	OutputTokens      int                                  `json:"output_tokens"`
	TotalTokens       int                                  `json:"total_tokens"`
}

type AudioTranscriptionInputTokenDetails struct {
	TextTokens  int `json:"text_tokens"`
	AudioTokens int `json:"audio_tokens"`
}
