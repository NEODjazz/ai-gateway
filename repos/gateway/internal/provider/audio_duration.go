package provider

import (
	"encoding/binary"
	"math"

	"ai-gateway-gateway/internal/openai"
)

func wavDurationMilliseconds(data []byte) (int, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0, openai.ErrInvalidAudio
	}
	byteRate := uint32(0)
	dataSize := uint32(0)
	for offset := 12; offset+8 <= len(data); {
		size := binary.LittleEndian.Uint32(data[offset+4 : offset+8])
		start := offset + 8
		end64 := uint64(start) + uint64(size)
		if end64 > uint64(len(data)) {
			return 0, openai.ErrInvalidAudio
		}
		end := int(end64)
		switch string(data[offset : offset+4]) {
		case "fmt ":
			if size < 16 {
				return 0, openai.ErrInvalidAudio
			}
			byteRate = binary.LittleEndian.Uint32(data[start+8 : start+12])
		case "data":
			dataSize = size
		}
		offset = end + int(size&1)
	}
	if byteRate == 0 || dataSize == 0 {
		return 0, openai.ErrInvalidAudio
	}
	return durationMilliseconds(uint64(dataSize), uint64(byteRate))
}

func flacDurationMilliseconds(data []byte) (int, error) {
	if len(data) < 42 || string(data[:4]) != "fLaC" || data[4]&0x7f != 0 {
		return 0, openai.ErrInvalidAudio
	}
	blockLength := int(data[5])<<16 | int(data[6])<<8 | int(data[7])
	if blockLength != 34 || len(data) < 8+blockLength {
		return 0, openai.ErrInvalidAudio
	}
	packed := binary.BigEndian.Uint64(data[18:26])
	sampleRate := packed >> 44
	totalSamples := packed & ((uint64(1) << 36) - 1)
	if sampleRate == 0 || totalSamples == 0 {
		return 0, openai.ErrInvalidAudio
	}
	return durationMilliseconds(totalSamples, sampleRate)
}

func durationMilliseconds(units, unitsPerSecond uint64) (int, error) {
	if unitsPerSecond == 0 || units > (math.MaxUint64-uint64(unitsPerSecond)+1)/1000 {
		return 0, openai.ErrInvalidAudio
	}
	milliseconds := (units*1000 + unitsPerSecond - 1) / unitsPerSecond
	if milliseconds == 0 || milliseconds > math.MaxInt {
		return 0, openai.ErrInvalidAudio
	}
	return int(milliseconds), nil
}
