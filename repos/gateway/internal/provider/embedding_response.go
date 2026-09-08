package provider

import (
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
	inputs, ok := openai.EmbeddingInputStrings(request.Input)
	if !ok || len(data) != len(inputs) {
		return errors.New("embedding response count does not match input")
	}
	seen := make([]bool, len(data))
	dimensions := 0
	for _, item := range data {
		if item.Index < 0 || item.Index >= len(data) || seen[item.Index] {
			return errors.New("invalid embedding index")
		}
		seen[item.Index] = true
		if dimensions == 0 {
			dimensions = len(item.Embedding)
		}
		if dimensions == 0 || dimensions > 65536 || len(item.Embedding) != dimensions || (request.Dimensions != nil && dimensions != *request.Dimensions) {
			return errors.New("invalid embedding dimensions")
		}
		for _, value := range item.Embedding {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return errors.New("invalid embedding value")
			}
		}
	}
	return nil
}
