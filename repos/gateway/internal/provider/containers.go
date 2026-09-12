package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"unicode/utf8"

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
	if err := p.ValidateContainerCreateParameters(input); err != nil {
		return result, err
	}
	err := p.containerJSONRequest(ctx, http.MethodPost, "containers", input, &result)
	if err == nil {
		err = validateContainer(result)
	}
	return result, err
}

func (OpenAICompatible) ValidateContainerCreateParameters(input openai.ContainerProviderCreateRequest) error {
	return validateContainerCreateRequest(input)
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
	if err := validateContainerExpiry(input.ExpiresAfter); err != nil {
		return err
	}
	return ValidateContainerNetworkPolicyRequest(input.NetworkPolicy)
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
	if !validResponseResourceID(result.ID) || result.Object != "container" || result.CreatedAt < 0 || result.LastActiveAt < 0 || result.Name == "" || len(result.Name) > 256 || result.Status == "" || len(result.Status) > 64 || len(result.MemoryLimit) > 16 || validateContainerExpiry(result.ExpiresAfter) != nil || validateContainerNetworkPolicy(result.NetworkPolicy) != nil {
		return errors.New("invalid upstream container response")
	}
	return nil
}

func ValidateContainerNetworkPolicyRequest(policy *openai.ContainerNetworkPolicyRequest) error {
	if policy == nil {
		return nil
	}
	if policy.Type == "disabled" {
		if len(policy.AllowedDomains) != 0 || len(policy.DomainSecrets) != 0 {
			return errors.New("disabled container network policy cannot include domains")
		}
		return nil
	}
	if policy.Type != "allowlist" || len(policy.AllowedDomains) == 0 || len(policy.AllowedDomains) > 100 || len(policy.DomainSecrets) > 100 {
		return errors.New("invalid container network policy")
	}
	allowed := make(map[string]struct{}, len(policy.AllowedDomains))
	for _, domain := range policy.AllowedDomains {
		if !validContainerDomain(domain) {
			return errors.New("invalid container network domain")
		}
		key := strings.ToLower(domain)
		if _, duplicate := allowed[key]; duplicate {
			return errors.New("duplicate container network domain")
		}
		allowed[key] = struct{}{}
	}
	secretNames := make(map[string]struct{}, len(policy.DomainSecrets))
	for _, secret := range policy.DomainSecrets {
		if _, ok := allowed[strings.ToLower(secret.Domain)]; !ok || !validContainerSecretText(secret.Name, 128) || !validContainerSecretText(secret.Value, 8192) {
			return errors.New("invalid container network domain secret")
		}
		key := strings.ToLower(secret.Domain) + "\x00" + secret.Name
		if _, duplicate := secretNames[key]; duplicate {
			return errors.New("duplicate container network domain secret")
		}
		secretNames[key] = struct{}{}
	}
	return nil
}

func validateContainerNetworkPolicy(policy *openai.ContainerNetworkPolicy) error {
	if policy == nil {
		return nil
	}
	request := &openai.ContainerNetworkPolicyRequest{Type: policy.Type, AllowedDomains: policy.AllowedDomains}
	return ValidateContainerNetworkPolicyRequest(request)
}

func validContainerDomain(value string) bool {
	if len(value) == 0 || len(value) > 253 || net.ParseIP(value) != nil || strings.HasSuffix(value, ".") {
		return false
	}
	if strings.HasPrefix(value, "*.") {
		value = value[2:]
	}
	labels := strings.Split(value, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func validContainerSecretText(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
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
