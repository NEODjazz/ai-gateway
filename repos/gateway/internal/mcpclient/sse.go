package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"ai-gateway-gateway/internal/publichttp"
)

const LegacyProtocolVersion = "2024-11-05"

type SSEClient struct {
	endpoint     *url.URL
	postEndpoint *url.URL
	http         *http.Client
	bearer       string
	ctx          context.Context
	cancel       context.CancelFunc
	responses    chan rpcResponse
	streamErrors chan error
	mu           sync.Mutex
	ready        bool
	nextID       uint64
}

func NewSSE(endpoint, bearer string) (*SSEClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !validBearerCredential(bearer) {
		return nil, errors.New("MCP SSE endpoint must be an HTTPS URL without credentials, query, or fragment")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &SSEClient{endpoint: parsed, http: publichttp.NewClient(0), bearer: bearer, ctx: ctx, cancel: cancel, responses: make(chan rpcResponse, 1), streamErrors: make(chan error, 1)}, nil
}

func (c *SSEClient) Close() error {
	c.cancel()
	return nil
}

func (c *SSEClient) Initialize(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ready {
		return nil
	}
	if err := c.openStreamLocked(ctx); err != nil {
		return err
	}
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := c.callLocked(ctx, "initialize", map[string]any{"protocolVersion": LegacyProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "ai-gateway", "version": "1"}}, &result); err != nil {
		return err
	}
	if result.ProtocolVersion != LegacyProtocolVersion {
		return fmt.Errorf("unsupported MCP protocol version %q", result.ProtocolVersion)
	}
	if err := c.notifyLocked(ctx, "notifications/initialized"); err != nil {
		return err
	}
	c.ready = true
	return nil
}

func (c *SSEClient) openStreamLocked(ctx context.Context) error {
	request, err := http.NewRequestWithContext(c.ctx, http.MethodGet, c.endpoint.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("MCP-Protocol-Version", LegacyProtocolVersion)
	c.setAuthorization(request)
	type responseResult struct {
		response *http.Response
		err      error
	}
	result := make(chan responseResult, 1)
	go func() {
		response, requestErr := c.http.Do(request)
		result <- responseResult{response: response, err: requestErr}
	}()
	var response *http.Response
	select {
	case <-ctx.Done():
		c.cancel()
		return ctx.Err()
	case opened := <-result:
		if opened.err != nil {
			return opened.err
		}
		response = opened.response
	}
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		_ = response.Body.Close()
		return fmt.Errorf("MCP SSE server returned HTTP %d without an event stream", response.StatusCode)
	}
	endpoint := make(chan string, 1)
	go c.readStream(response.Body, endpoint)
	select {
	case <-ctx.Done():
		c.cancel()
		return ctx.Err()
	case err := <-c.streamErrors:
		return err
	case value := <-endpoint:
		resolved, err := c.resolvePostEndpoint(value)
		if err != nil {
			c.cancel()
			return err
		}
		c.postEndpoint = resolved
		return nil
	}
}

func (c *SSEClient) resolvePostEndpoint(value string) (*url.URL, error) {
	if value == "" || len(value) > 4096 {
		return nil, errors.New("invalid MCP SSE message endpoint")
	}
	for _, character := range value {
		if character < ' ' || character == '\x7f' {
			return nil, errors.New("invalid MCP SSE message endpoint")
		}
	}
	reference, err := url.Parse(value)
	if err != nil {
		return nil, errors.New("invalid MCP SSE message endpoint")
	}
	resolved := c.endpoint.ResolveReference(reference)
	if resolved.Scheme != c.endpoint.Scheme || !strings.EqualFold(resolved.Host, c.endpoint.Host) || resolved.User != nil || resolved.Fragment != "" {
		return nil, errors.New("MCP SSE message endpoint must use the configured origin")
	}
	return resolved, nil
}

func (c *SSEClient) readStream(body io.ReadCloser, endpoint chan<- string) {
	defer body.Close()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), maxBodyBytes)
	eventName := ""
	var data strings.Builder
	endpointSeen := false
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data.Len() > 0 {
				value := data.String()
				if !endpointSeen && eventName != "endpoint" {
					c.reportStreamError(errors.New("MCP SSE stream did not start with an endpoint event"))
					return
				}
				switch eventName {
				case "endpoint":
					if endpointSeen {
						c.reportStreamError(errors.New("duplicate MCP SSE endpoint event"))
						return
					}
					endpointSeen = true
					endpoint <- value
				case "message":
					var response rpcResponse
					if json.Unmarshal([]byte(value), &response) != nil || response.JSONRPC != "2.0" {
						c.reportStreamError(errors.New("invalid MCP SSE message"))
						return
					}
					if response.Method != "" || len(response.ID) == 0 {
						continue
					}
					select {
					case c.responses <- response:
					case <-c.ctx.Done():
						return
					}
				}
			}
			eventName = ""
			data.Reset()
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			separatorBytes := 0
			if data.Len() > 0 {
				separatorBytes = 1
			}
			if data.Len()+separatorBytes+len(value) > maxBodyBytes {
				c.reportStreamError(errors.New("MCP SSE event exceeds limit"))
				return
			}
			if separatorBytes != 0 {
				data.WriteByte('\n')
			}
			data.WriteString(value)
		}
	}
	if err := scanner.Err(); err != nil {
		c.reportStreamError(err)
	} else if c.ctx.Err() == nil {
		c.reportStreamError(errors.New("MCP SSE stream ended"))
	}
}

func (c *SSEClient) reportStreamError(err error) {
	select {
	case c.streamErrors <- err:
	default:
	}
}

func (c *SSEClient) ListTools(ctx context.Context, cursor string) (ToolPage, error) {
	if len(cursor) > 2048 {
		return ToolPage{}, errors.New("MCP cursor exceeds limit")
	}
	if err := c.Initialize(ctx); err != nil {
		return ToolPage{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	params := map[string]any{}
	if cursor != "" {
		params["cursor"] = cursor
	}
	var page ToolPage
	if err := c.callLocked(ctx, "tools/list", params, &page); err != nil {
		return ToolPage{}, err
	}
	if err := validateToolPage(page); err != nil {
		return ToolPage{}, err
	}
	return page, nil
}

func (c *SSEClient) CallTool(ctx context.Context, name string, arguments map[string]any) (CallResult, error) {
	if name == "" || len(name) > 256 {
		return CallResult{}, errors.New("invalid MCP tool name")
	}
	if err := c.Initialize(ctx); err != nil {
		return CallResult{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var result CallResult
	if err := c.callLocked(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments}, &result); err != nil {
		return CallResult{}, err
	}
	if err := validateCallResult(result); err != nil {
		return CallResult{}, err
	}
	return result, nil
}

func (c *SSEClient) callLocked(ctx context.Context, method string, params any, destination any) error {
	c.nextID++
	id := c.nextID
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil || len(payload) > maxRequestBytes {
		return errors.New("MCP request exceeds limit")
	}
	if err := c.postLocked(ctx, payload); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-c.streamErrors:
		return err
	case response := <-c.responses:
		if response.JSONRPC != "2.0" || string(response.ID) != strconv.FormatUint(id, 10) {
			return errors.New("MCP response ID does not match request")
		}
		if response.Error != nil && len(response.Result) > 0 {
			return errors.New("MCP response contains both result and error")
		}
		if response.Error != nil {
			return fmt.Errorf("MCP error %d: %s", response.Error.Code, response.Error.Message)
		}
		if len(response.Result) == 0 || json.Unmarshal(response.Result, destination) != nil {
			return errors.New("invalid MCP result")
		}
		return nil
	}
}

func (c *SSEClient) notifyLocked(ctx context.Context, method string) error {
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if err != nil {
		return err
	}
	return c.postLocked(ctx, payload)
}

func (c *SSEClient) postLocked(ctx context.Context, payload []byte) error {
	if c.postEndpoint == nil {
		return errors.New("MCP SSE message endpoint is unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.postEndpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("MCP-Protocol-Version", LegacyProtocolVersion)
	c.setAuthorization(request)
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("MCP SSE message endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (c *SSEClient) setAuthorization(request *http.Request) {
	if c.bearer != "" {
		request.Header.Set("Authorization", "Bearer "+c.bearer)
	}
}
