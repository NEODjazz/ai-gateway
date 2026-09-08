package provider

import (
	"context"
	"errors"
	"net/http"

	"ai-gateway-gateway/internal/openai"
)

// RetrieveResponse reads an upstream resource. The caller must authorize its
// owner and select the original deployment before invoking this transport method.
func (p OpenAICompatible) RetrieveResponse(ctx context.Context, id string) (openai.ResponseResponse, error) {
	if !validResponseResourceID(id) {
		return openai.ResponseResponse{}, &Error{Class: FailureClientRequest, StatusCode: 400, UpstreamCode: "invalid_request", Param: "response_id", Err: errors.New("invalid response ID")}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, providerURL(p.baseURL, "responses/"+id), nil)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	request.Header.Set("Accept", "application/json")
	if p.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	client := *p.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.ResponseResponse{}, responseStatusError("openai-compatible", response)
	}
	result, err := decodeResponseJSON(response.Body)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	if result.ID != id {
		return openai.ResponseResponse{}, errors.New("upstream returned a different response ID")
	}
	return result, nil
}

func validResponseResourceID(id string) bool {
	if len(id) == 0 || len(id) > 256 {
		return false
	}
	for _, c := range id {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}
