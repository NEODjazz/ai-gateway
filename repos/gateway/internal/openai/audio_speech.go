package openai

import (
	"strings"
	"unicode/utf8"
)

const MaxSpeechInputCharacters = 4096

type AudioSpeechRequest struct {
	Provider       string   `json:"provider,omitempty"`
	Model          string   `json:"model"`
	Input          string   `json:"input"`
	Voice          string   `json:"voice"`
	Instructions   string   `json:"instructions,omitempty"`
	ResponseFormat string   `json:"response_format,omitempty"`
	Speed          *float64 `json:"speed,omitempty"`
	StreamFormat   string   `json:"stream_format,omitempty"`
}

type AudioSpeechResponse struct {
	Data        []byte `json:"-"`
	ContentType string `json:"-"`
	Model       string `json:"-"`
}

func (r AudioSpeechRequest) Validate() string {
	if strings.TrimSpace(r.Model) == "" {
		return "model is required"
	}
	if strings.TrimSpace(r.Input) == "" {
		return "input is required"
	}
	if !utf8.ValidString(r.Input) || utf8.RuneCountInString(r.Input) > MaxSpeechInputCharacters {
		return "input must contain at most 4096 Unicode characters"
	}
	if strings.TrimSpace(r.Voice) == "" || len(r.Voice) > 128 {
		return "voice is required and must contain at most 128 bytes"
	}
	if len(r.Instructions) > 16<<10 {
		return "instructions exceed the 16 KiB limit"
	}
	if r.Speed != nil && (*r.Speed < 0.25 || *r.Speed > 4) {
		return "speed must be between 0.25 and 4"
	}
	if r.ResponseFormat != "" && !speechResponseFormats[r.ResponseFormat] {
		return "response_format must be mp3, opus, aac, flac, wav, or pcm"
	}
	if r.StreamFormat != "" && r.StreamFormat != "audio" {
		return "stream_format must be audio"
	}
	return ""
}

func (r AudioSpeechRequest) InputCharacters() int {
	return utf8.RuneCountInString(r.Input)
}

func AudioSpeechReserveTokens(r AudioSpeechRequest) int {
	return ReserveTokens(EstimateContextTokens(struct {
		Input        string
		Voice        string
		Instructions string
	}{r.Input, r.Voice, r.Instructions}), 0)
}

func (r AudioSpeechRequest) ExpectedContentType() string {
	format := r.ResponseFormat
	if format == "" {
		format = "mp3"
	}
	return map[string]string{
		"mp3": "audio/mpeg", "opus": "audio/ogg", "aac": "audio/aac",
		"flac": "audio/flac", "wav": "audio/wav", "pcm": "application/octet-stream",
	}[format]
}

var speechResponseFormats = map[string]bool{"mp3": true, "opus": true, "aac": true, "flac": true, "wav": true, "pcm": true}
