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

func oggPage(flags byte, granule uint64, serial, sequence uint32, body []byte) []byte {
	page := make([]byte, 28+len(body))
	copy(page[:4], "OggS")
	page[5] = flags
	binary.LittleEndian.PutUint64(page[6:14], granule)
	binary.LittleEndian.PutUint32(page[14:18], serial)
	binary.LittleEndian.PutUint32(page[18:22], sequence)
	page[26] = 1
	page[27] = byte(len(body))
	copy(page[28:], body)
	return page
}

func oggOpusAttachment(milliseconds int) openai.AudioAttachment {
	const preSkip = 312
	header := make([]byte, 19)
	copy(header, "OpusHead")
	header[8], header[9] = 1, 1
	binary.LittleEndian.PutUint16(header[10:12], preSkip)
	payload := append(oggPage(0x02, 0, 17, 0, header), oggPage(0x04, uint64(milliseconds*48+preSkip), 17, 1, []byte{0xf8})...)
	return openai.AudioAttachment{Filename: "meeting.ogg", MediaType: "audio/ogg", Data: base64.StdEncoding.EncodeToString(payload)}
}

func oggVorbisAttachment(milliseconds int) openai.AudioAttachment {
	const sampleRate = 8000
	header := make([]byte, 30)
	copy(header, "\x01vorbis")
	header[11] = 1
	binary.LittleEndian.PutUint32(header[12:16], sampleRate)
	header[28], header[29] = 0x76, 1
	payload := append(oggPage(0x02, 0, 23, 0, header), oggPage(0x04, uint64(milliseconds*sampleRate/1000), 23, 1, []byte{0})...)
	return openai.AudioAttachment{Filename: "meeting.ogg", MediaType: "audio/ogg", Data: base64.StdEncoding.EncodeToString(payload)}
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

func TestOGGDurationMilliseconds(t *testing.T) {
	for _, attachment := range []openai.AudioAttachment{oggOpusAttachment(1250), oggVorbisAttachment(1250)} {
		data, err := base64.StdEncoding.DecodeString(attachment.Data)
		if err != nil {
			t.Fatal(err)
		}
		if duration, err := oggDurationMilliseconds(data); err != nil || duration != 1250 {
			t.Fatalf("duration=%d err=%v", duration, err)
		}
	}
	opus, _ := base64.StdEncoding.DecodeString(oggOpusAttachment(1250).Data)
	invalidSerial := append([]byte(nil), opus...)
	firstPageLength := 28 + 19
	binary.LittleEndian.PutUint32(invalidSerial[firstPageLength+14:firstPageLength+18], 99)
	for _, invalid := range [][]byte{nil, opus[:len(opus)-1], invalidSerial, append([]byte(nil), opus[:firstPageLength]...)} {
		if _, err := oggDurationMilliseconds(invalid); err == nil {
			t.Fatalf("invalid OGG accepted: %x", invalid)
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

func TestProvidersReserveExactOGGDuration(t *testing.T) {
	for _, attachment := range []openai.AudioAttachment{oggOpusAttachment(12500), oggVorbisAttachment(12500)} {
		request := openai.AudioTranscriptionRequest{File: attachment}
		if duration, err := NewGroq("https://example.test", "", false).ReserveAudioMilliseconds(request); err != nil || duration != 12500 {
			t.Fatalf("Groq duration=%d err=%v", duration, err)
		}
		if duration, err := NewMistral("https://example.test", "", false).ReserveAudioMilliseconds(request); err != nil || duration != 12500 {
			t.Fatalf("Mistral duration=%d err=%v", duration, err)
		}
	}
}
