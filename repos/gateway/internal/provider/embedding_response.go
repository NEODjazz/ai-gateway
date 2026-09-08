package provider

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"

	"ai-gateway-gateway/internal/openai"
)

const maxEmbeddingResponseBytes = 32 << 20

func decodeEmbeddingResponse(reader io.Reader, target any) error {
	payload, err := io.ReadAll(io.LimitReader(reader, maxEmbeddingResponseBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > maxEmbeddingResponseBytes {
		return errors.New("embedding response exceeds limit")
	}
	return json.Unmarshal(payload, target)
}

func validateEmbeddingVectors(request openai.EmbeddingRequest, data []openai.Embedding) error {
	input, err := openai.InspectEmbeddingInput(request.Input)
	if err != nil || len(data) != input.Count {
		return errors.New("embedding response count does not match input")
	}
	seen := make([]bool, len(data))
	dimensions := 0
	for _, item := range data {
		if item.Index < 0 || item.Index >= len(data) || seen[item.Index] {
			return errors.New("invalid embedding index")
		}
		seen[item.Index] = true
		itemDimensions := len(item.Embedding)
		if request.EncodingFormat == "base64" {
			if len(item.Embedding) != 0 || item.EmbeddingBase64 == "" {
				return errors.New("provider returned an embedding in the wrong encoding format")
			}
			decoded, err := base64.StdEncoding.Strict().DecodeString(item.EmbeddingBase64)
			if err != nil || len(decoded) == 0 || len(decoded)%4 != 0 || len(decoded) > 65536*4 {
				return errors.New("invalid base64 embedding")
			}
			itemDimensions = len(decoded) / 4
			for offset := 0; offset < len(decoded); offset += 4 {
				value := math.Float32frombits(binary.LittleEndian.Uint32(decoded[offset : offset+4]))
				if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
					return errors.New("invalid embedding value")
				}
			}
		} else {
			if item.EmbeddingBase64 != "" {
				return errors.New("provider returned an embedding in the wrong encoding format")
			}
			for _, value := range item.Embedding {
				if math.IsNaN(value) || math.IsInf(value, 0) {
					return errors.New("invalid embedding value")
				}
			}
		}
		if dimensions == 0 {
			dimensions = itemDimensions
		}
		if dimensions == 0 || dimensions > 65536 || itemDimensions != dimensions || (request.Dimensions != nil && dimensions != *request.Dimensions) {
			return errors.New("invalid embedding dimensions")
		}
	}
	return nil
}
