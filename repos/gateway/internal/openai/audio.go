package openai

import (
	"encoding/base64"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const MaxAudioBytes = 20 << 20

const maxAudioTranscriptRunes = 1 << 20

type AudioAttachment struct {
	Filename  string `json:"filename"`
	MediaType string `json:"media_type"`
	Data      string `json:"data_base64"`
}

type AudioTranscriptionRequest struct {
	Provider               string          `json:"provider,omitempty"`
	Model                  string          `json:"model"`
	File                   AudioAttachment `json:"file"`
	Language               string          `json:"language,omitempty"`
	Prompt                 string          `json:"prompt,omitempty"`
	ResponseFormat         string          `json:"response_format,omitempty"`
	Temperature            *float64        `json:"temperature,omitempty"`
	TimestampGranularities []string        `json:"timestamp_granularities,omitempty"`
	Include                []string        `json:"include,omitempty"`
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
	return ""
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
	promptTokens := EstimateContextTokens(r.Prompt)
	audioTokens := bytesTokenEstimate(data)
	maxInt := int(^uint(0) >> 1)
	if promptTokens > maxInt-audioTokens {
		return maxInt
	}
	return promptTokens + audioTokens
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
