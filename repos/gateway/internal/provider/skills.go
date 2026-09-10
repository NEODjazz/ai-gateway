package provider

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"ai-gateway-gateway/internal/modules"
)

const MaxSkillRequestBytes = 32 << 20
const MaxSkillResponseBytes = 8 << 20

type SkillRequest struct {
	Method      string
	Path        string
	RawQuery    string
	ContentType string
	Body        []byte
}

type SkillResponse struct {
	StatusCode  int
	ContentType string
	Body        []byte
}

type SkillClient interface {
	ExecuteSkillRequest(context.Context, SkillRequest) (SkillResponse, error)
}

type SkillProvider interface {
	ExecuteSkillRequest(context.Context, modules.RequestContext, string, SkillRequest) (SkillResponse, string, error)
}

func (Anthropic) SupportsSkills() bool { return true }

func (p Anthropic) ExecuteSkillRequest(ctx context.Context, request SkillRequest) (SkillResponse, error) {
	if !validSkillTransportRequest(request) {
		return SkillResponse{}, &Error{Class: FailureClientRequest, Provider: "anthropic", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New("invalid Skills request")}
	}
	httpRequest, err := http.NewRequestWithContext(ctx, request.Method, providerURL(p.baseURL, request.Path), bytes.NewReader(request.Body))
	if err != nil {
		return SkillResponse{}, err
	}
	httpRequest.URL.RawQuery = request.RawQuery
	httpRequest.Header.Set("anthropic-version", "2023-06-01")
	if request.ContentType != "" {
		httpRequest.Header.Set("Content-Type", request.ContentType)
	}
	if p.apiKey != "" {
		httpRequest.Header.Set("x-api-key", p.apiKey)
	}
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return SkillResponse{}, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, MaxSkillResponseBytes+1))
	if err != nil {
		return SkillResponse{}, err
	}
	if len(payload) > MaxSkillResponseBytes {
		return SkillResponse{}, errors.New("Skills response exceeds the 8 MiB limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return SkillResponse{}, statusError("anthropic", response.StatusCode)
	}
	return SkillResponse{StatusCode: response.StatusCode, ContentType: response.Header.Get("Content-Type"), Body: payload}, nil
}

func validSkillTransportRequest(request SkillRequest) bool {
	if len(request.Body) > MaxSkillRequestBytes || strings.ContainsAny(request.RawQuery, "\r\n") {
		return false
	}
	if request.Method != http.MethodGet && request.Method != http.MethodPost && request.Method != http.MethodDelete {
		return false
	}
	parts := strings.Split(strings.Trim(request.Path, "/"), "/")
	if len(parts) < 1 || len(parts) > 5 || parts[0] != "skills" {
		return false
	}
	for _, part := range parts[1:] {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, character := range part {
			if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
				continue
			}
			return false
		}
	}
	isVersions := len(parts) >= 3 && parts[2] == "versions"
	switch request.Method {
	case http.MethodGet:
		return len(parts) <= 2 || isVersions && (len(parts) == 3 || len(parts) == 4 || len(parts) == 5 && parts[4] == "content")
	case http.MethodPost:
		return len(parts) == 1 || isVersions && len(parts) == 3
	case http.MethodDelete:
		return len(parts) == 2 || isVersions && len(parts) == 4
	default:
		return false
	}
}

func (r Router) ExecuteSkillRequest(ctx context.Context, _ modules.RequestContext, endpointID string, request SkillRequest) (SkillResponse, string, error) {
	for _, endpoint := range r.configuredEndpoints() {
		if endpointID != "" && endpoint.Name != endpointID || endpoint.Type != "anthropic" || !hasCapability(endpoint.Capabilities, "skills") {
			continue
		}
		client, ok := endpoint.Provider.(SkillClient)
		if !ok || !r.health.available(ctx, endpoint) {
			continue
		}
		release, err := r.acquireEndpoint(ctx, endpoint, 0)
		if err != nil {
			return SkillResponse{}, endpoint.Name, err
		}
		response, callErr := client.ExecuteSkillRequest(ctx, request)
		release()
		if callErr != nil {
			r.health.failure(ctx, endpoint, callErr)
			return SkillResponse{}, endpoint.Name, callErr
		}
		r.health.success(ctx, endpoint)
		return response, endpoint.Name, nil
	}
	return SkillResponse{}, "", errors.New("no available Skills endpoint")
}
