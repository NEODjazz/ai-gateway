package provider

import (
	"encoding/json"
	"errors"
	"io"

	"ai-gateway-gateway/internal/openai"
)

const maxResponseJSONBytes = 32 << 20

func decodeResponseJSON(reader io.Reader) (openai.ResponseResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxResponseJSONBytes+1))
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	if len(payload) > maxResponseJSONBytes {
		return openai.ResponseResponse{}, errors.New("upstream Responses JSON exceeds 32 MiB")
	}
	var response *openai.ResponseResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return openai.ResponseResponse{}, err
	}
	if response == nil {
		return openai.ResponseResponse{}, errors.New("upstream Responses JSON must be an object")
	}
	if err := validateResponseUsage(response.Usage); err != nil {
		return openai.ResponseResponse{}, err
	}
	response.OutputText = responseText(*response)
	return *response, nil
}
