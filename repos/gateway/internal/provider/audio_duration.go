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

func oggDurationMilliseconds(data []byte) (int, error) {
	var (
		offset       int
		serial       uint32
		nextSequence uint32
		sampleRate   uint64
		preSkip      uint64
		lastGranule  uint64
		seenEOS      bool
	)
	for offset < len(data) {
		if len(data)-offset < 27 || string(data[offset:offset+4]) != "OggS" || data[offset+4] != 0 {
			return 0, openai.ErrInvalidAudio
		}
		flags := data[offset+5]
		granule := binary.LittleEndian.Uint64(data[offset+6 : offset+14])
		pageSerial := binary.LittleEndian.Uint32(data[offset+14 : offset+18])
		sequence := binary.LittleEndian.Uint32(data[offset+18 : offset+22])
		segments := int(data[offset+26])
		headerEnd := offset + 27 + segments
		if headerEnd > len(data) {
			return 0, openai.ErrInvalidAudio
		}
		bodyLength := 0
		for _, length := range data[offset+27 : headerEnd] {
			bodyLength += int(length)
		}
		pageEnd := headerEnd + bodyLength
		if pageEnd > len(data) || seenEOS {
			return 0, openai.ErrInvalidAudio
		}
		if offset == 0 {
			if flags&0x02 == 0 || sequence != 0 || flags&0x01 != 0 {
				return 0, openai.ErrInvalidAudio
			}
			serial = pageSerial
			packetLength := 0
			packetComplete := false
			for _, length := range data[offset+27 : headerEnd] {
				packetLength += int(length)
				if length < 255 {
					packetComplete = true
					break
				}
			}
			if !packetComplete || packetLength != bodyLength {
				return 0, openai.ErrInvalidAudio
			}
			packet := data[headerEnd:pageEnd]
			switch {
			case len(packet) >= 19 && string(packet[:8]) == "OpusHead" && packet[8] <= 15 && packet[9] > 0:
				sampleRate = 48000
				preSkip = uint64(binary.LittleEndian.Uint16(packet[10:12]))
			case len(packet) >= 30 && string(packet[:7]) == "\x01vorbis" && binary.LittleEndian.Uint32(packet[7:11]) == 0 && packet[11] > 0:
				sampleRate = uint64(binary.LittleEndian.Uint32(packet[12:16]))
				shortBlock, longBlock := packet[28]&0x0f, packet[28]>>4
				if sampleRate == 0 || shortBlock < 6 || longBlock > 13 || shortBlock > longBlock || packet[29]&1 == 0 {
					return 0, openai.ErrInvalidAudio
				}
			default:
				return 0, openai.ErrInvalidAudio
			}
		} else if pageSerial != serial || sequence != nextSequence || flags&0x02 != 0 {
			return 0, openai.ErrInvalidAudio
		}
		if granule != math.MaxUint64 {
			if granule < lastGranule {
				return 0, openai.ErrInvalidAudio
			}
			lastGranule = granule
		}
		seenEOS = flags&0x04 != 0
		nextSequence = sequence + 1
		offset = pageEnd
	}
	if !seenEOS || sampleRate == 0 || lastGranule <= preSkip {
		return 0, openai.ErrInvalidAudio
	}
	return durationMilliseconds(lastGranule-preSkip, sampleRate)
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
