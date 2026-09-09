package openai

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	MaxChatAudioDataChars       = 32 << 20
	MaxChatAudioTranscriptChars = 1 << 20
)

type ChatAudioOptions struct {
	Format string         `json:"format"`
	Voice  ChatAudioVoice `json:"voice"`
}

type ChatAudioVoice struct {
	Name string `json:"-"`
	ID   string `json:"-"`
}

func (v *ChatAudioVoice) UnmarshalJSON(data []byte) error {
	if len(data) == 0 {
		return errors.New("audio voice is required")
	}
	if data[0] == '"' {
		if err := json.Unmarshal(data, &v.Name); err != nil {
			return err
		}
		v.ID = ""
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var custom struct {
		ID string `json:"id"`
	}
	if err := decoder.Decode(&custom); err != nil {
		return err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	v.Name, v.ID = "", custom.ID
	return nil
}

func (v ChatAudioVoice) MarshalJSON() ([]byte, error) {
	if v.ID != "" {
		return json.Marshal(struct {
			ID string `json:"id"`
		}{v.ID})
	}
	return json.Marshal(v.Name)
}

func (v ChatAudioVoice) valid() bool {
	return (v.Name == "") != (v.ID == "") && utf8.RuneCountInString(v.Name+v.ID) <= 256
}

type ChatAudio struct {
	ID         string  `json:"id"`
	Data       *string `json:"data,omitempty"`
	ExpiresAt  *int64  `json:"expires_at,omitempty"`
	Transcript *string `json:"transcript,omitempty"`
}

func ChatRequestsAudio(request ChatCompletionRequest) bool {
	for _, modality := range request.Modalities {
		if modality == "audio" {
			return true
		}
	}
	return false
}

func ChatHasAudioHistory(request ChatCompletionRequest) bool {
	for _, message := range request.Messages {
		if message.Audio != nil {
			return true
		}
	}
	return false
}

func validateChatAudioOptions(options *ChatAudioOptions) string {
	if options == nil {
		return "audio output requires audio format and voice"
	}
	switch options.Format {
	case "wav", "aac", "mp3", "flac", "opus", "pcm16":
	default:
		return "audio.format must be wav, aac, mp3, flac, opus, or pcm16"
	}
	if !options.Voice.valid() {
		return "audio.voice must be a non-empty string or an object containing one bounded id"
	}
	return ""
}

func ValidateChatAudioReference(audio *ChatAudio) error {
	if audio == nil {
		return nil
	}
	if audio.ID == "" || utf8.RuneCountInString(audio.ID) > 256 || audio.Data != nil || audio.ExpiresAt != nil || audio.Transcript != nil {
		return errors.New("messages.audio must contain only a bounded previous audio id")
	}
	return nil
}

func ValidateChatAudioResponse(audio *ChatAudio) error {
	if audio == nil {
		return nil
	}
	if audio.ID == "" || utf8.RuneCountInString(audio.ID) > 256 || audio.Data == nil || audio.ExpiresAt == nil || *audio.ExpiresAt < 0 || audio.Transcript == nil {
		return errors.New("incomplete chat audio response")
	}
	if len(*audio.Data) == 0 || len(*audio.Data) > MaxChatAudioDataChars || utf8.RuneCountInString(*audio.Transcript) > MaxChatAudioTranscriptChars {
		return errors.New("chat audio response exceeds limits")
	}
	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(*audio.Data))
	if _, err := io.Copy(io.Discard, decoder); err != nil {
		return fmt.Errorf("invalid chat audio base64 data: %w", err)
	}
	return nil
}

func ValidateChatAudioDelta(audio *ChatAudio) error {
	if audio == nil {
		return nil
	}
	if utf8.RuneCountInString(audio.ID) > 256 || audio.ExpiresAt != nil && *audio.ExpiresAt < 0 {
		return errors.New("invalid chat audio delta")
	}
	if audio.Data != nil {
		if len(*audio.Data) > MaxChatAudioDataChars {
			return errors.New("chat audio delta exceeds limits")
		}
		for _, character := range *audio.Data {
			if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=", character) {
				return errors.New("invalid chat audio base64 fragment")
			}
		}
	}
	if audio.Transcript != nil && utf8.RuneCountInString(*audio.Transcript) > MaxChatAudioTranscriptChars {
		return errors.New("chat audio delta exceeds limits")
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON")
		}
		return err
	}
	return nil
}
