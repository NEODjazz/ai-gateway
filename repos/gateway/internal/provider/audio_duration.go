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

func mp3DurationMilliseconds(data []byte) (int, error) {
	offset := 0
	if len(data) >= 10 && string(data[:3]) == "ID3" {
		majorVersion := data[3]
		if majorVersion < 2 || majorVersion > 4 || data[4] == 0xff || (majorVersion != 4 && data[5]&0x10 != 0) {
			return 0, openai.ErrInvalidAudio
		}
		if data[6]&0x80 != 0 || data[7]&0x80 != 0 || data[8]&0x80 != 0 || data[9]&0x80 != 0 {
			return 0, openai.ErrInvalidAudio
		}
		tagSize := int(data[6])<<21 | int(data[7])<<14 | int(data[8])<<7 | int(data[9])
		offset = 10 + tagSize
		if majorVersion == 4 && data[5]&0x10 != 0 {
			offset += 10
		}
		if offset > len(data) {
			return 0, openai.ErrInvalidAudio
		}
	}
	var totalSamples uint64
	sampleRate := 0
	frames := 0
	for offset < len(data) {
		if len(data)-offset == 128 && string(data[offset:offset+3]) == "TAG" {
			offset = len(data)
			break
		}
		frameLength, frameSamples, frameSampleRate, ok := mp3Frame(data[offset:])
		if !ok || frameLength > len(data)-offset || (sampleRate != 0 && frameSampleRate != sampleRate) {
			return 0, openai.ErrInvalidAudio
		}
		sampleRate = frameSampleRate
		if totalSamples > math.MaxUint64-uint64(frameSamples) {
			return 0, openai.ErrInvalidAudio
		}
		totalSamples += uint64(frameSamples)
		frames++
		offset += frameLength
	}
	if frames == 0 || offset != len(data) {
		return 0, openai.ErrInvalidAudio
	}
	return durationMilliseconds(totalSamples, uint64(sampleRate))
}

func mp3Frame(data []byte) (length, samples, sampleRate int, ok bool) {
	if len(data) < 4 || data[0] != 0xff || data[1]&0xe0 != 0xe0 {
		return 0, 0, 0, false
	}
	version := (data[1] >> 3) & 0x03
	layer := (data[1] >> 1) & 0x03
	bitrateIndex := (data[2] >> 4) & 0x0f
	sampleRateIndex := (data[2] >> 2) & 0x03
	if version == 1 || layer != 1 || bitrateIndex == 0 || bitrateIndex == 15 || sampleRateIndex == 3 {
		return 0, 0, 0, false
	}
	mpeg1Bitrates := [...]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
	mpeg2Bitrates := [...]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}
	sampleRates := [...]int{44100, 48000, 32000}
	sampleRate = sampleRates[sampleRateIndex]
	bitrate := 0
	coefficient := 72000
	samples = 576
	switch version {
	case 3:
		bitrate = mpeg1Bitrates[bitrateIndex]
		coefficient = 144000
		samples = 1152
	case 2:
		bitrate = mpeg2Bitrates[bitrateIndex]
		sampleRate /= 2
	case 0:
		bitrate = mpeg2Bitrates[bitrateIndex]
		sampleRate /= 4
	}
	padding := int((data[2] >> 1) & 1)
	length = coefficient*bitrate/sampleRate + padding
	return length, samples, sampleRate, length >= 4
}

func mp4DurationMilliseconds(data []byte) (int, error) {
	topLevel, err := mp4Atoms(data)
	if err != nil {
		return 0, err
	}
	foundFileType := false
	var movie []byte
	for _, atom := range topLevel {
		switch atom.kind {
		case "ftyp":
			foundFileType = true
		case "moov":
			if movie != nil {
				return 0, openai.ErrInvalidAudio
			}
			movie = atom.content
		}
	}
	if !foundFileType || movie == nil {
		return 0, openai.ErrInvalidAudio
	}
	duration, found, err := mp4FirstAudioTrackDuration(movie)
	if err != nil || !found {
		return 0, openai.ErrInvalidAudio
	}
	return duration, nil
}

type mp4Atom struct {
	kind    string
	content []byte
}

func mp4Atoms(data []byte) ([]mp4Atom, error) {
	const maxMP4Atoms = 100000
	atoms := make([]mp4Atom, 0, 8)
	for offset := 0; offset < len(data); {
		if len(atoms) >= maxMP4Atoms {
			return nil, openai.ErrInvalidAudio
		}
		if len(data)-offset < 8 {
			return nil, openai.ErrInvalidAudio
		}
		size := uint64(binary.BigEndian.Uint32(data[offset : offset+4]))
		headerSize := uint64(8)
		if size == 1 {
			if len(data)-offset < 16 {
				return nil, openai.ErrInvalidAudio
			}
			size = binary.BigEndian.Uint64(data[offset+8 : offset+16])
			headerSize = 16
		} else if size == 0 {
			size = uint64(len(data) - offset)
		}
		if size < headerSize || size > uint64(len(data)-offset) {
			return nil, openai.ErrInvalidAudio
		}
		end := offset + int(size)
		atoms = append(atoms, mp4Atom{kind: string(data[offset+4 : offset+8]), content: data[offset+int(headerSize) : end]})
		offset = end
	}
	return atoms, nil
}

func mp4FirstAudioTrackDuration(movie []byte) (int, bool, error) {
	atoms, err := mp4Atoms(movie)
	if err != nil {
		return 0, false, err
	}
	for _, atom := range atoms {
		if atom.kind != "trak" {
			continue
		}
		trackAtoms, err := mp4Atoms(atom.content)
		if err != nil {
			return 0, false, err
		}
		mediaCount := 0
		for _, trackAtom := range trackAtoms {
			if trackAtom.kind != "mdia" {
				continue
			}
			mediaCount++
			if mediaCount > 1 {
				return 0, false, openai.ErrInvalidAudio
			}
			mediaAtoms, err := mp4Atoms(trackAtom.content)
			if err != nil {
				return 0, false, err
			}
			isAudio := false
			handlers := 0
			var mediaHeader []byte
			for _, mediaAtom := range mediaAtoms {
				switch mediaAtom.kind {
				case "hdlr":
					handlers++
					if handlers > 1 || len(mediaAtom.content) < 12 {
						return 0, false, openai.ErrInvalidAudio
					}
					isAudio = string(mediaAtom.content[8:12]) == "soun"
				case "mdhd":
					if mediaHeader != nil {
						return 0, false, openai.ErrInvalidAudio
					}
					mediaHeader = mediaAtom.content
				}
			}
			if isAudio {
				duration, err := mp4MediaDurationMilliseconds(mediaHeader)
				return duration, true, err
			}
		}
	}
	return 0, false, nil
}

func mp4MediaDurationMilliseconds(header []byte) (int, error) {
	if len(header) < 20 {
		return 0, openai.ErrInvalidAudio
	}
	var timescale, duration uint64
	switch header[0] {
	case 0:
		timescale = uint64(binary.BigEndian.Uint32(header[12:16]))
		duration = uint64(binary.BigEndian.Uint32(header[16:20]))
		if duration == math.MaxUint32 {
			return 0, openai.ErrInvalidAudio
		}
	case 1:
		if len(header) < 32 {
			return 0, openai.ErrInvalidAudio
		}
		timescale = uint64(binary.BigEndian.Uint32(header[20:24]))
		duration = binary.BigEndian.Uint64(header[24:32])
		if duration == math.MaxUint64 {
			return 0, openai.ErrInvalidAudio
		}
	default:
		return 0, openai.ErrInvalidAudio
	}
	return durationMilliseconds(duration, timescale)
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
