package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"ai-gateway-gateway/internal/openai"
)

// RetrieveResponse reads an upstream resource. The caller must authorize its
// owner and select the original deployment before invoking this transport method.
func (p OpenAICompatible) RetrieveResponse(ctx context.Context, id string) (openai.ResponseResponse, error) {
	return p.responseResourceRequest(ctx, http.MethodGet, id, "")
}

func (p OpenAICompatible) ListResponseInputItems(ctx context.Context, id string, options ResponseInputItemsOptions) (openai.ResponseInputItemList, error) {
	if !validResponseResourceID(id) {
		return openai.ResponseInputItemList{}, &Error{Class: FailureClientRequest, StatusCode: 400, UpstreamCode: "invalid_request", Param: "response_id", Err: errors.New("invalid response ID")}
	}
	if err := validateResponseInputItemsOptions(options); err != nil {
		return openai.ResponseInputItemList{}, &Error{Class: FailureClientRequest, StatusCode: 400, UpstreamCode: "invalid_request", Param: "pagination", Err: err}
	}
	query := url.Values{}
	if options.After != "" {
		query.Set("after", options.After)
	}
	if options.Limit > 0 {
		query.Set("limit", strconv.Itoa(options.Limit))
	}
	if options.Order != "" {
		query.Set("order", options.Order)
	}
	for _, include := range options.Include {
		query.Add("include", include)
	}
	requestURL := providerURL(p.baseURL, "responses/"+id+"/input_items")
	if encoded := query.Encode(); encoded != "" {
		requestURL += "?" + encoded
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, http.NoBody)
	if err != nil {
		return openai.ResponseInputItemList{}, err
	}
	request.Header.Set("Accept", "application/json")
	if p.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	client := *p.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return openai.ResponseInputItemList{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.ResponseInputItemList{}, responseStatusError("openai-compatible", response)
	}
	return decodeResponseInputItems(response.Body)
}

func decodeResponseInputItems(reader io.Reader) (openai.ResponseInputItemList, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxResponseJSONBytes+1))
	if err != nil {
		return openai.ResponseInputItemList{}, err
	}
	if len(payload) > maxResponseJSONBytes {
		return openai.ResponseInputItemList{}, errors.New("upstream response input items exceed 32 MiB")
	}
	var result *openai.ResponseInputItemList
	if err := json.Unmarshal(payload, &result); err != nil {
		return openai.ResponseInputItemList{}, err
	}
	if result == nil || result.Object != "list" || result.Data == nil || len(result.Data) > 10000 || len(result.FirstID) > 256 || len(result.LastID) > 256 {
		return openai.ResponseInputItemList{}, errors.New("invalid upstream response input items")
	}
	for _, item := range result.Data {
		trimmed := bytes.TrimSpace(item)
		if len(trimmed) < 2 || trimmed[0] != '{' {
			return openai.ResponseInputItemList{}, errors.New("upstream response input item must be an object")
		}
	}
	return *result, nil
}

func validateResponseInputItemsOptions(options ResponseInputItemsOptions) error {
	if options.After != "" && !validResponseResourceQueryToken(options.After) {
		return errors.New("invalid after cursor")
	}
	if options.Limit < 0 || options.Limit > 100 || options.Order != "" && options.Order != "asc" && options.Order != "desc" || len(options.Include) > 16 {
		return errors.New("invalid input-items pagination")
	}
	for _, include := range options.Include {
		if !validResponseResourceQueryToken(include) {
			return errors.New("invalid include value")
		}
	}
	return nil
}

func validResponseResourceQueryToken(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

// CancelResponse requests cancellation of an upstream background response.
// Ownership and deployment authorization remain the caller's responsibility.
func (p OpenAICompatible) CancelResponse(ctx context.Context, id string) (openai.ResponseResponse, error) {
	return p.responseResourceRequest(ctx, http.MethodPost, id, "cancel")
}

func (p OpenAICompatible) DeleteResponse(ctx context.Context, id string) (openai.ResponseDeletion, error) {
	if !validResponseResourceID(id) {
		return openai.ResponseDeletion{}, &Error{Class: FailureClientRequest, StatusCode: 400, UpstreamCode: "invalid_request", Param: "response_id", Err: errors.New("invalid response ID")}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, providerURL(p.baseURL, "responses/"+id), http.NoBody)
	if err != nil {
		return openai.ResponseDeletion{}, err
	}
	request.Header.Set("Accept", "application/json")
	if p.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	client := *p.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return openai.ResponseDeletion{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.ResponseDeletion{}, responseStatusError("openai-compatible", response)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil {
		return openai.ResponseDeletion{}, err
	}
	if len(payload) > 64<<10 {
		return openai.ResponseDeletion{}, errors.New("upstream response deletion exceeds 64 KiB")
	}
	var result *openai.ResponseDeletion
	if err := json.Unmarshal(payload, &result); err != nil {
		return openai.ResponseDeletion{}, err
	}
	if result == nil || result.ID != id || result.Object != "response.deleted" || !result.Deleted {
		return openai.ResponseDeletion{}, errors.New("invalid upstream response deletion")
	}
	return *result, nil
}

func (p OpenAICompatible) responseResourceRequest(ctx context.Context, method, id, action string) (openai.ResponseResponse, error) {
	if !validResponseResourceID(id) {
		return openai.ResponseResponse{}, &Error{Class: FailureClientRequest, StatusCode: 400, UpstreamCode: "invalid_request", Param: "response_id", Err: errors.New("invalid response ID")}
	}
	path := "responses/" + id
	if action != "" {
		path += "/" + action
	}
	request, err := http.NewRequestWithContext(ctx, method, providerURL(p.baseURL, path), http.NoBody)
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
