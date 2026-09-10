package provider

import (
	"encoding/base64"
	"encoding/binary"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func flacAttachment(milliseconds int) openai.AudioAttachment {
	const sampleRate = 8000
	totalSamples := uint64(sampleRate * milliseconds / 1000)
	payload := make([]byte, 42)
	copy(payload[:4], "fLaC")
	payload[4] = 0x80
	payload[7] = 34
	packed := uint64(sampleRate)<<44 | uint64(7)<<36 | totalSamples
	binary.BigEndian.PutUint64(payload[18:26], packed)
	return openai.AudioAttachment{Filename: "meeting.flac", MediaType: "audio/flac", Data: base64.StdEncoding.EncodeToString(payload)}
}

func TestFLACDurationMilliseconds(t *testing.T) {
	attachment := flacAttachment(1250)
	data, err := base64.StdEncoding.DecodeString(attachment.Data)
	if err != nil {
		t.Fatal(err)
	}
	if duration, err := flacDurationMilliseconds(data); err != nil || duration != 1250 {
		t.Fatalf("duration=%d err=%v", duration, err)
	}
	for _, invalid := range [][]byte{nil, []byte("fLaC"), append([]byte("fLaC\x80\x00\x00\x22"), make([]byte, 34)...)} {
		if _, err := flacDurationMilliseconds(invalid); err == nil {
			t.Fatalf("invalid FLAC accepted: %x", invalid)
		}
	}
}

func TestProvidersReserveExactFLACDuration(t *testing.T) {
	request := openai.AudioTranscriptionRequest{File: flacAttachment(1250)}
	if duration, err := NewGroq("https://example.test", "", false).ReserveAudioMilliseconds(request); err != nil || duration != 10000 {
		t.Fatalf("Groq duration=%d err=%v", duration, err)
	}
	if duration, err := NewMistral("https://example.test", "", false).ReserveAudioMilliseconds(request); err != nil || duration != 1250 {
		t.Fatalf("Mistral duration=%d err=%v", duration, err)
	}
}
