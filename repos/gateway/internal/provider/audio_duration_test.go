package provider

import (
	"encoding/base64"
	"encoding/binary"
	"math"
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

func mp3Attachment(frames int) openai.AudioAttachment {
	payload := make([]byte, 10)
	copy(payload, "ID3\x04\x00\x00\x00\x00\x00\x00")
	for index := 0; index < frames; index++ {
		bitrateIndex := byte(9)
		if index%2 == 1 {
			bitrateIndex = 10
		}
		header := []byte{0xff, 0xfb, bitrateIndex << 4, 0}
		frameLength, _, _, ok := mp3Frame(header)
		if !ok {
			panic("invalid MP3 test frame")
		}
		frame := make([]byte, frameLength)
		copy(frame, header)
		payload = append(payload, frame...)
	}
	return openai.AudioAttachment{Filename: "meeting.mp3", MediaType: "audio/mpeg", Data: base64.StdEncoding.EncodeToString(payload)}
}

func mp4AtomBytes(kind string, content ...[]byte) []byte {
	size := 8
	for _, part := range content {
		size += len(part)
	}
	atom := make([]byte, size)
	binary.BigEndian.PutUint32(atom[:4], uint32(size))
	copy(atom[4:8], kind)
	offset := 8
	for _, part := range content {
		copy(atom[offset:], part)
		offset += len(part)
	}
	return atom
}

func mp4Track(handler string, timescale uint32, duration uint64, version byte) []byte {
	handlerHeader := make([]byte, 12)
	copy(handlerHeader[8:12], handler)
	mediaHeader := make([]byte, 20)
	mediaHeader[0] = version
	if version == 0 {
		binary.BigEndian.PutUint32(mediaHeader[12:16], timescale)
		binary.BigEndian.PutUint32(mediaHeader[16:20], uint32(duration))
	} else {
		mediaHeader = make([]byte, 32)
		mediaHeader[0] = version
		binary.BigEndian.PutUint32(mediaHeader[20:24], timescale)
		binary.BigEndian.PutUint64(mediaHeader[24:32], duration)
	}
	media := mp4AtomBytes("mdia", mp4AtomBytes("mdhd", mediaHeader), mp4AtomBytes("hdlr", handlerHeader))
	return mp4AtomBytes("trak", media)
}

func mp4Attachment(timescale uint32, duration uint64, version byte) openai.AudioAttachment {
	fileType := mp4AtomBytes("ftyp", []byte("isom\x00\x00\x00\x00"))
	movie := mp4AtomBytes("moov", mp4Track("vide", 1000, 60000, 0), mp4Track("soun", timescale, duration, version))
	payload := append(fileType, movie...)
	return openai.AudioAttachment{Filename: "meeting.m4a", MediaType: "audio/mp4", Data: base64.StdEncoding.EncodeToString(payload)}
}

func ebmlSize(value int) []byte {
	if value < 0x7f {
		return []byte{0x80 | byte(value)}
	}
	if value < 0x3fff {
		return []byte{0x40 | byte(value>>8), byte(value)}
	}
	panic("test EBML element is too large")
}

func ebmlElementBytes(id []byte, content ...[]byte) []byte {
	length := 0
	for _, part := range content {
		length += len(part)
	}
	element := append([]byte(nil), id...)
	element = append(element, ebmlSize(length)...)
	for _, part := range content {
		element = append(element, part...)
	}
	return element
}

func ebmlUnsignedBytes(value uint64) []byte {
	width := 1
	for width < 8 && value >= uint64(1)<<(8*width) {
		width++
	}
	result := make([]byte, width)
	for index := width - 1; index >= 0; index-- {
		result[index] = byte(value)
		value >>= 8
	}
	return result
}

func webmAttachment(duration float64, timestampScale uint64, trackTypes ...byte) openai.AudioAttachment {
	header := ebmlElementBytes([]byte{0x1a, 0x45, 0xdf, 0xa3}, ebmlElementBytes([]byte{0x42, 0x82}, []byte("webm")))
	durationValue := make([]byte, 8)
	binary.BigEndian.PutUint64(durationValue, math.Float64bits(duration))
	infoParts := [][]byte{ebmlElementBytes([]byte{0x44, 0x89}, durationValue)}
	if timestampScale != 1000000 {
		infoParts = append(infoParts, ebmlElementBytes([]byte{0x2a, 0xd7, 0xb1}, ebmlUnsignedBytes(timestampScale)))
	}
	info := ebmlElementBytes([]byte{0x15, 0x49, 0xa9, 0x66}, infoParts...)
	trackEntries := make([][]byte, 0, len(trackTypes))
	for _, trackType := range trackTypes {
		trackEntries = append(trackEntries, ebmlElementBytes([]byte{0xae}, ebmlElementBytes([]byte{0x83}, []byte{trackType})))
	}
	tracks := ebmlElementBytes([]byte{0x16, 0x54, 0xae, 0x6b}, trackEntries...)
	segment := ebmlElementBytes([]byte{0x18, 0x53, 0x80, 0x67}, info, tracks)
	payload := append(header, segment...)
	return openai.AudioAttachment{Filename: "meeting.webm", MediaType: "audio/webm", Data: base64.StdEncoding.EncodeToString(payload)}
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

func TestMP3DurationMilliseconds(t *testing.T) {
	attachment := mp3Attachment(2)
	data, err := base64.StdEncoding.DecodeString(attachment.Data)
	if err != nil {
		t.Fatal(err)
	}
	if duration, err := mp3DurationMilliseconds(data); err != nil || duration != 53 {
		t.Fatalf("duration=%d err=%v", duration, err)
	}
	truncated := data[:len(data)-1]
	mixedRate := append([]byte(nil), data...)
	firstLength, _, _, _ := mp3Frame(mixedRate[10:])
	mixedRate[10+firstLength+2] |= 0x04
	for _, invalid := range [][]byte{nil, []byte("ID3payload"), truncated, mixedRate} {
		if _, err := mp3DurationMilliseconds(invalid); err == nil {
			t.Fatalf("invalid MP3 accepted: %x", invalid)
		}
	}
}

func TestMP3FrameVersions(t *testing.T) {
	tests := []struct {
		header     []byte
		length     int
		samples    int
		sampleRate int
	}{
		{header: []byte{0xff, 0xfb, 0x90, 0}, length: 417, samples: 1152, sampleRate: 44100},
		{header: []byte{0xff, 0xf3, 0x80, 0}, length: 208, samples: 576, sampleRate: 22050},
		{header: []byte{0xff, 0xe3, 0x80, 0}, length: 417, samples: 576, sampleRate: 11025},
	}
	for _, test := range tests {
		length, samples, sampleRate, ok := mp3Frame(test.header)
		if !ok || length != test.length || samples != test.samples || sampleRate != test.sampleRate {
			t.Fatalf("header=%x length=%d samples=%d sampleRate=%d ok=%t", test.header, length, samples, sampleRate, ok)
		}
	}
}

func TestMP4AudioTrackDurationMilliseconds(t *testing.T) {
	for _, attachment := range []openai.AudioAttachment{mp4Attachment(1000, 1250, 0), mp4Attachment(100000, 125000, 1)} {
		data, err := base64.StdEncoding.DecodeString(attachment.Data)
		if err != nil {
			t.Fatal(err)
		}
		if duration, err := mp4DurationMilliseconds(data); err != nil || duration != 1250 {
			t.Fatalf("duration=%d err=%v", duration, err)
		}
	}
}

func TestMP4AudioTrackDurationRejectsMalformedMetadata(t *testing.T) {
	valid, _ := base64.StdEncoding.DecodeString(mp4Attachment(1000, 1250, 0).Data)
	fileType := mp4AtomBytes("ftyp", []byte("isom\x00\x00\x00\x00"))
	onlyVideo := append(fileType, mp4AtomBytes("moov", mp4Track("vide", 1000, 60000, 0))...)
	missingHeader := append(fileType, mp4AtomBytes("moov", mp4AtomBytes("trak", mp4AtomBytes("mdia", mp4AtomBytes("hdlr", append(make([]byte, 8), []byte("soun")...)))))...)
	duplicateMovie := append(append([]byte(nil), valid...), mp4AtomBytes("moov")...)
	zeroTimescale, _ := base64.StdEncoding.DecodeString(mp4Attachment(0, 1250, 0).Data)
	unknownDuration, _ := base64.StdEncoding.DecodeString(mp4Attachment(1000, math.MaxUint32, 0).Data)
	for _, invalid := range [][]byte{nil, valid[:len(valid)-1], onlyVideo, missingHeader, duplicateMovie, zeroTimescale, unknownDuration, []byte("\x00\x00\x00\x04ftyp")} {
		if _, err := mp4DurationMilliseconds(invalid); err == nil {
			t.Fatalf("invalid MP4 accepted: %x", invalid)
		}
	}
}

func TestWebMAudioDurationMilliseconds(t *testing.T) {
	for _, attachment := range []openai.AudioAttachment{webmAttachment(1250, 1000000, 2), webmAttachment(1.25, 1000000000, 2)} {
		data, err := base64.StdEncoding.DecodeString(attachment.Data)
		if err != nil {
			t.Fatal(err)
		}
		if duration, err := webmAudioDurationMilliseconds(data); err != nil || duration != 1250 {
			t.Fatalf("duration=%d err=%v", duration, err)
		}
	}
	unknownSegmentSize, _ := base64.StdEncoding.DecodeString(webmAttachment(1250, 1000000, 2).Data)
	for offset := 0; offset+5 <= len(unknownSegmentSize); offset++ {
		if string(unknownSegmentSize[offset:offset+4]) == "\x18\x53\x80\x67" {
			unknownSegmentSize[offset+4] = 0xff
			break
		}
	}
	if duration, err := webmAudioDurationMilliseconds(unknownSegmentSize); err != nil || duration != 1250 {
		t.Fatalf("unknown segment size duration=%d err=%v", duration, err)
	}
}

func TestWebMAudioDurationRejectsAmbiguousOrMalformedMetadata(t *testing.T) {
	valid, _ := base64.StdEncoding.DecodeString(webmAttachment(1250, 1000000, 2).Data)
	video, _ := base64.StdEncoding.DecodeString(webmAttachment(1250, 1000000, 1).Data)
	multiplexed, _ := base64.StdEncoding.DecodeString(webmAttachment(1250, 1000000, 2, 1).Data)
	notFinite, _ := base64.StdEncoding.DecodeString(webmAttachment(math.NaN(), 1000000, 2).Data)
	for _, invalid := range [][]byte{nil, valid[:len(valid)-1], video, multiplexed, notFinite, []byte{0x1a, 0x45, 0xdf, 0xa3, 0xff}} {
		if _, err := webmAudioDurationMilliseconds(invalid); err == nil {
			t.Fatalf("invalid WebM accepted: %x", invalid)
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

func TestProvidersReserveExactMP3Duration(t *testing.T) {
	request := openai.AudioTranscriptionRequest{File: mp3Attachment(479)}
	const expected = 12513
	if duration, err := NewGroq("https://example.test", "", false).ReserveAudioMilliseconds(request); err != nil || duration != expected {
		t.Fatalf("Groq duration=%d err=%v", duration, err)
	}
	if duration, err := NewMistral("https://example.test", "", false).ReserveAudioMilliseconds(request); err != nil || duration != expected {
		t.Fatalf("Mistral duration=%d err=%v", duration, err)
	}
}

func TestGroqReservesExactMP4AudioTrackDuration(t *testing.T) {
	client := NewGroq("https://example.test", "", false)
	if duration, err := client.ReserveAudioMilliseconds(openai.AudioTranscriptionRequest{File: mp4Attachment(1000, 1250, 0)}); err != nil || duration != 10000 {
		t.Fatalf("minimum duration=%d err=%v", duration, err)
	}
	if duration, err := client.ReserveAudioMilliseconds(openai.AudioTranscriptionRequest{File: mp4Attachment(1000, 12500, 0)}); err != nil || duration != 12500 {
		t.Fatalf("exact duration=%d err=%v", duration, err)
	}
}

func TestProvidersReserveWebMAudioDuration(t *testing.T) {
	request := openai.AudioTranscriptionRequest{File: webmAttachment(12500, 1000000, 2)}
	if duration, err := NewGroq("https://example.test", "", false).ReserveAudioMilliseconds(request); err != nil || duration != 12500 {
		t.Fatalf("Groq duration=%d err=%v", duration, err)
	}
	if duration, err := NewMistral("https://example.test", "", false).ReserveAudioMilliseconds(request); err != nil || duration != 12500 {
		t.Fatalf("Mistral duration=%d err=%v", duration, err)
	}
}
