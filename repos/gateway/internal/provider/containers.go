package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

const maxContainerMetadataBytes = 1 << 20

type ContainerClient interface {
	CreateContainer(context.Context, openai.ContainerProviderCreateRequest) (openai.Container, error)
	RetrieveContainer(context.Context, string) (openai.Container, error)
	DeleteContainer(context.Context, string) (openai.ContainerDeletion, error)
}

func (p OpenAICompatible) CreateContainer(ctx context.Context, input openai.ContainerProviderCreateRequest) (openai.Container, error) {
	var result openai.Container
	if err := validateContainerCreateRequest(input); err != nil {
		return result, err
	}
	err := p.containerJSONRequest(ctx, http.MethodPost, "containers", input, &result)
	if err == nil {
		err = validateContainer(result)
	}
	return result, err
}

func (p OpenAICompatible) RetrieveContainer(ctx context.Context, id string) (openai.Container, error) {
	var result openai.Container
	if !validResponseResourceID(id) {
		return result, errors.New("invalid container ID")
	}
	err := p.containerJSONRequest(ctx, http.MethodGet, "containers/"+id, nil, &result)
	if err == nil {
		err = validateContainer(result)
	}
	return result, err
}

func (p OpenAICompatible) DeleteContainer(ctx context.Context, id string) (openai.ContainerDeletion, error) {
	var result openai.ContainerDeletion
	if !validResponseResourceID(id) {
		return result, errors.New("invalid container ID")
	}
	err := p.containerJSONRequest(ctx, http.MethodDelete, "containers/"+id, nil, &result)
	if err == nil && (result.ID != id || result.Object != "container.deleted" || !result.Deleted) {
		err = errors.New("invalid upstream container deletion")
	}
	return result, err
}

func validateContainerCreateRequest(input openai.ContainerProviderCreateRequest) error {
	if strings.TrimSpace(input.Name) != input.Name || input.Name == "" || len(input.Name) > 256 {
		return errors.New("invalid container name")
	}
	if input.MemoryLimit != "" && input.MemoryLimit != "1g" && input.MemoryLimit != "4g" && input.MemoryLimit != "16g" && input.MemoryLimit != "64g" {
		return errors.New("invalid container memory limit")
	}
	return validateContainerExpiry(input.ExpiresAfter)
}

func validateContainerExpiry(expiry *openai.ContainerExpiresAfter) error {
	if expiry == nil {
		return nil
	}
	if expiry.Anchor != "last_active_at" || expiry.Minutes < 1 || expiry.Minutes > 10080 {
		return errors.New("invalid container expiration")
	}
	return nil
}

func validateContainer(result openai.Container) error {
	if !validResponseResourceID(result.ID) || result.Object != "container" || result.CreatedAt < 0 || result.LastActiveAt < 0 || result.Name == "" || len(result.Name) > 256 || result.Status == "" || len(result.Status) > 64 || len(result.MemoryLimit) > 16 || validateContainerExpiry(result.ExpiresAfter) != nil {
		return errors.New("invalid upstream container response")
	}
	return nil
}

func (p OpenAICompatible) containerJSONRequest(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader = http.NoBody
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, providerURL(p.baseURL, path), body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if p.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	client := *p.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseStatusError(p.providerName(), response)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxContainerMetadataBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > maxContainerMetadataBytes {
		return errors.New("upstream container response exceeds 1 MiB")
	}
	if err = json.Unmarshal(payload, output); err != nil {
		return errors.New("invalid upstream container response")
	}
	return nil
}
