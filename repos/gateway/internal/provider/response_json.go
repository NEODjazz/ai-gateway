package provider

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"

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
	if err := recordResponseInputUsage(payload, response); err != nil {
		return openai.ResponseResponse{}, err
	}
	if err := validateResponseUsage(response.Usage); err != nil {
		return openai.ResponseResponse{}, err
	}
	if err := validateResponseOutputItems(response.Output); err != nil {
		return openai.ResponseResponse{}, err
	}
	response.OutputText = responseText(*response)
	return *response, nil
}

func validateResponseOutputItems(items []openai.ResponseOutputItem) error {
	for _, item := range items {
		if item.Type != "image_generation_call" {
			continue
		}
		if len(item.Result) == 0 {
			return errors.New("provider image generation call is missing result")
		}
		if string(item.Result) == "null" {
			continue
		}
		var encoded string
		if json.Unmarshal(item.Result, &encoded) != nil || encoded == "" {
			return errors.New("provider image generation result must be base64 or null")
		}
		decoder := base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(encoded))
		if _, err := io.Copy(io.Discard, decoder); err != nil {
			return errors.New("provider image generation result is malformed base64")
		}
	}
	return nil
}
