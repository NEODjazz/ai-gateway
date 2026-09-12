package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-gateway-gateway/internal/openai"
)

const (
	defaultSandboxTemplate = "opensandbox/code-interpreter:v1.1.0"
	defaultSandboxTimeout  = 300
	maxSandboxOutputBytes  = 10 << 20
	maxSandboxMetadata     = 1 << 20
	sandboxExecPort        = 44772
)

type SandboxExecuteRequest struct {
	Code                string
	Language            string
	Template            string
	TimeoutSeconds      int
	AllowInternetAccess bool
}

type SandboxExecutionResult struct {
	Stdout         string           `json:"stdout"`
	Stderr         string           `json:"stderr"`
	Results        []map[string]any `json:"results"`
	Error          map[string]any   `json:"error,omitempty"`
	ExecutionCount *int             `json:"execution_count,omitempty"`
	Object         string           `json:"object"`
}

type SandboxClient interface {
	ExecuteSandbox(context.Context, SandboxExecuteRequest) (SandboxExecutionResult, error)
}

type OpenSandbox struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func NewOpenSandbox(baseURL, apiKey string) OpenSandbox {
	return OpenSandbox{baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), apiKey: apiKey, client: newProviderHTTPClient(180 * time.Second)}
}

func (OpenSandbox) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, sandboxUnsupported("chat completions")
}

func (OpenSandbox) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, sandboxUnsupported("Responses")
}

func (OpenSandbox) SupportsChat() bool      { return false }
func (OpenSandbox) SupportsResponses() bool { return false }

func sandboxUnsupported(operation string) error {
	return &Error{Class: FailureClientRequest, Provider: "opensandbox", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_operation", Err: fmt.Errorf("%s are not supported by this adapter", operation)}
}

func (p OpenSandbox) ExecuteSandbox(ctx context.Context, request SandboxExecuteRequest) (result SandboxExecutionResult, err error) {
	if err = validateSandboxExecuteRequest(request); err != nil {
		return result, &Error{Class: FailureClientRequest, Provider: "opensandbox", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: err}
	}
	sandboxID, err := p.createSandbox(ctx, request)
	if err != nil {
		return result, err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if cleanupErr := p.deleteSandbox(cleanupCtx, sandboxID); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("delete sandbox %s: %w", sandboxID, cleanupErr))
		}
	}()
	if err = p.waitForSandbox(ctx, sandboxID, request.TimeoutSeconds); err != nil {
		return result, err
	}
	endpoint, headers, err := p.executionEndpoint(ctx, sandboxID)
	if err != nil {
		return result, err
	}
	return p.runSandboxCode(ctx, endpoint, headers, request)
}

func validateSandboxExecuteRequest(request SandboxExecuteRequest) error {
	if request.Code == "" || len(request.Code) > 1<<20 || request.Language == "" || len(request.Language) > 32 || request.Template == "" || len(request.Template) > 512 || request.TimeoutSeconds < 1 || request.TimeoutSeconds > 900 {
		return errors.New("invalid sandbox execution request")
	}
	for _, value := range []string{request.Language, request.Template} {
		if strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("invalid sandbox execution request")
		}
	}
	return nil
}

func (p OpenSandbox) createSandbox(ctx context.Context, request SandboxExecuteRequest) (string, error) {
	body := map[string]any{
		"image":          map[string]string{"uri": request.Template},
		"entrypoint":     []string{"/opt/code-interpreter/code-interpreter.sh"},
		"timeout":        request.TimeoutSeconds,
		"resourceLimits": map[string]string{"cpu": "1", "memory": "2Gi"},
	}
	if !request.AllowInternetAccess {
		body["networkPolicy"] = map[string]any{"defaultAction": "deny", "egress": []any{}}
	}
	var response struct {
		ID string `json:"id"`
	}
	if err := p.jsonRequest(ctx, http.MethodPost, p.baseURL+"/sandboxes", body, &response); err != nil {
		return "", err
	}
	if !validResponseResourceID(response.ID) {
		return "", errors.New("sandbox provider returned an invalid ID")
	}
	return response.ID, nil
}

func (p OpenSandbox) waitForSandbox(ctx context.Context, id string, timeoutSeconds int) error {
	deadline := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
	for {
		var response struct {
			Status struct {
				State string `json:"state"`
			} `json:"status"`
		}
		if err := p.jsonRequest(ctx, http.MethodGet, p.baseURL+"/sandboxes/"+id, nil, &response); err != nil {
			return err
		}
		switch response.Status.State {
		case "Running":
			return nil
		case "Failed", "Stopping", "Terminated":
			return fmt.Errorf("sandbox entered terminal state %s", response.Status.State)
		}
		if time.Now().After(deadline) {
			return errors.New("sandbox readiness timeout")
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (p OpenSandbox) executionEndpoint(ctx context.Context, id string) (string, map[string]string, error) {
	var response struct {
		Endpoint string            `json:"endpoint"`
		Headers  map[string]string `json:"headers"`
	}
	path := fmt.Sprintf("%s/sandboxes/%s/endpoints/%d?use_server_proxy=true", p.baseURL, id, sandboxExecPort)
	if err := p.jsonRequest(ctx, http.MethodGet, path, nil, &response); err != nil {
		return "", nil, err
	}
	endpoint, err := p.validateExecutionEndpoint(response.Endpoint)
	if err != nil {
		return "", nil, err
	}
	return endpoint, response.Headers, nil
}

func (p OpenSandbox) validateExecutionEndpoint(raw string) (string, error) {
	base, baseErr := url.Parse(p.baseURL)
	if baseErr != nil || base.Hostname() == "" {
		return "", errors.New("invalid sandbox provider base URL")
	}
	if !strings.Contains(raw, "://") {
		raw = base.Scheme + "://" + raw
	}
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.User != nil || endpoint.Fragment != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" {
		return "", errors.New("sandbox provider returned an invalid execution endpoint")
	}
	baseHost, endpointHost := strings.ToLower(base.Hostname()), strings.ToLower(endpoint.Hostname())
	if endpointHost != baseHost && !strings.HasSuffix(endpointHost, "."+baseHost) {
		return "", errors.New("sandbox execution endpoint is outside the configured provider domain")
	}
	return strings.TrimRight(endpoint.String(), "/"), nil
}

func (p OpenSandbox) runSandboxCode(ctx context.Context, endpoint string, headers map[string]string, request SandboxExecuteRequest) (SandboxExecutionResult, error) {
	payload, err := json.Marshal(map[string]any{"code": request.Code, "context": map[string]string{"language": request.Language}})
	if err != nil {
		return SandboxExecutionResult{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/code", bytes.NewReader(payload))
	if err != nil {
		return SandboxExecutionResult{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	for name, value := range headers {
		if !validSandboxHeader(name, value) {
			return SandboxExecutionResult{}, errors.New("sandbox provider returned invalid execution headers")
		}
		httpRequest.Header.Set(name, value)
	}
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return SandboxExecutionResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return SandboxExecutionResult{}, responseStatusError("opensandbox", response)
	}
	body, err := readSandboxBounded(response.Body, maxSandboxOutputBytes)
	if err != nil {
		return SandboxExecutionResult{}, err
	}
	return decodeSandboxEvents(body)
}

func (p OpenSandbox) deleteSandbox(ctx context.Context, id string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, p.baseURL+"/sandboxes/"+id, nil)
	if err != nil {
		return err
	}
	p.setLifecycleHeaders(request)
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseStatusError("opensandbox", response)
	}
	return nil
}

func (p OpenSandbox) jsonRequest(ctx context.Context, method, endpoint string, input, output any) error {
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	p.setLifecycleHeaders(request)
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseStatusError("opensandbox", response)
	}
	payload, err := readSandboxBounded(response.Body, maxSandboxMetadata)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, output); err != nil {
		return errors.New("sandbox provider returned invalid JSON")
	}
	return nil
}

func (p OpenSandbox) setLifecycleHeaders(request *http.Request) {
	request.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		request.Header.Set("OPEN-SANDBOX-API-KEY", p.apiKey)
	}
}

func readSandboxBounded(reader io.Reader, limit int64) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > limit {
		return nil, errors.New("sandbox response exceeds limit")
	}
	return payload, nil
}

func validSandboxHeader(name, value string) bool {
	canonical := http.CanonicalHeaderKey(name)
	return canonical != "" && canonical != "Cookie" && canonical != "Host" && canonical != "Connection" && canonical != "Transfer-Encoding" && len(value) <= 8192 && !strings.ContainsAny(value, "\r\n")
}

func decodeSandboxEvents(payload []byte) (SandboxExecutionResult, error) {
	result := SandboxExecutionResult{Object: "code_execution", Results: []map[string]any{}}
	for _, line := range bytes.Split(payload, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || bytes.HasPrefix(line, []byte(":")) || bytes.HasPrefix(line, []byte("event:")) || bytes.HasPrefix(line, []byte("id:")) || bytes.HasPrefix(line, []byte("retry:")) {
			continue
		}
		line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		var event map[string]any
		if len(line) == 0 || json.Unmarshal(line, &event) != nil {
			continue
		}
		switch event["type"] {
		case "stdout":
			if text, ok := event["text"].(string); ok {
				result.Stdout += text
			}
		case "stderr":
			if text, ok := event["text"].(string); ok {
				result.Stderr += text
			}
		case "result":
			if values, ok := event["results"].(map[string]any); ok {
				result.Results = append(result.Results, values)
			} else {
				delete(event, "type")
				result.Results = append(result.Results, event)
			}
		case "error":
			if value, ok := event["error"].(map[string]any); ok {
				result.Error = value
			} else {
				delete(event, "type")
				result.Error = event
			}
		case "execution_count":
			if value, ok := event["execution_count"].(float64); ok {
				count := int(value)
				result.ExecutionCount = &count
			}
		}
	}
	return result, nil
}
