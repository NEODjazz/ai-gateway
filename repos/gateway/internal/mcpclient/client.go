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
	"time"

	"ai-gateway-gateway/internal/publichttp"
)

const (
	ProtocolVersion = "2025-06-18"
	maxRequestBytes = 1 << 20
	maxBodyBytes    = 8 << 20
	maxTools        = 1000
)

type Client struct {
	endpoint *url.URL
	http     *http.Client
	bearer   string
	mu       sync.Mutex
	session  string
	ready    bool
	nextID   uint64
}

type Tool struct {
	Name         string          `json:"name"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
}

type ToolPage struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
}

type CallResult struct {
	Content           []json.RawMessage `json:"content"`
	StructuredContent json.RawMessage   `json:"structuredContent,omitempty"`
	IsError           bool              `json:"isError,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func New(endpoint string) (*Client, error) {
	return NewWithBearer(endpoint, "")
}

func NewWithBearer(endpoint, bearer string) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || len(bearer) > 32768 || strings.TrimSpace(bearer) != bearer || strings.ContainsAny(bearer, "\r\n") {
		return nil, errors.New("MCP endpoint must be an HTTPS URL without credentials, query, or fragment")
	}
	return &Client{endpoint: parsed, http: publichttp.NewClient(30 * time.Second), bearer: bearer}, nil
}

func (c *Client) Initialize(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ready {
		return nil
	}
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	session, err := c.callLocked(ctx, "initialize", map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "ai-gateway", "version": "1"}}, &result)
	if err != nil {
		return err
	}
	if result.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("unsupported MCP protocol version %q", result.ProtocolVersion)
	}
	if session != "" && !validSessionID(session) {
		return errors.New("invalid MCP session ID")
	}
	c.session = session
	if err := c.notifyLocked(ctx, "notifications/initialized"); err != nil {
		c.session = ""
		return err
	}
	c.ready = true
	return nil
}

func (c *Client) ListTools(ctx context.Context, cursor string) (ToolPage, error) {
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
	if _, err := c.callLocked(ctx, "tools/list", params, &page); err != nil {
		return ToolPage{}, err
	}
	if len(page.Tools) > maxTools {
		return ToolPage{}, errors.New("MCP tool page exceeds limit")
	}
	if len(page.NextCursor) > 2048 {
		return ToolPage{}, errors.New("MCP cursor exceeds limit")
	}
	for _, tool := range page.Tools {
		if tool.Name == "" || len(tool.Name) > 256 || !validJSONObject(tool.InputSchema, true) || (len(tool.OutputSchema) > 0 && !validJSONObject(tool.OutputSchema, true)) || (len(tool.Annotations) > 0 && !validJSONObject(tool.Annotations, false)) {
			return ToolPage{}, errors.New("invalid MCP tool definition")
		}
	}
	return page, nil
}

func validJSONObject(raw json.RawMessage, requireObjectType bool) bool {
	var value map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || value == nil {
		return false
	}
	if !requireObjectType {
		return true
	}
	typeName, ok := value["type"].(string)
	return ok && typeName == "object"
}

func (c *Client) CallTool(ctx context.Context, name string, arguments map[string]any) (CallResult, error) {
	if name == "" || len(name) > 256 {
		return CallResult{}, errors.New("invalid MCP tool name")
	}
	if err := c.Initialize(ctx); err != nil {
		return CallResult{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var result CallResult
	if _, err := c.callLocked(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments}, &result); err != nil {
		return CallResult{}, err
	}
	if result.Content == nil || len(result.Content) > 1000 {
		return CallResult{}, errors.New("invalid MCP tool result")
	}
	for _, content := range result.Content {
		var object map[string]any
		if json.Unmarshal(content, &object) != nil || object == nil {
			return CallResult{}, errors.New("invalid MCP tool result content")
		}
	}
	if len(result.StructuredContent) > 0 {
		var object map[string]any
		if json.Unmarshal(result.StructuredContent, &object) != nil || object == nil {
			return CallResult{}, errors.New("invalid MCP structured tool result")
		}
	}
	return result, nil
}

func (c *Client) callLocked(ctx context.Context, method string, params any, destination any) (string, error) {
	c.nextID++
	id := c.nextID
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return "", err
	}
	if len(payload) > maxRequestBytes {
		return "", errors.New("MCP request exceeds limit")
	}
	response, session, err := c.post(ctx, payload)
	if err != nil {
		return "", err
	}
	if response.JSONRPC != "2.0" || string(response.ID) != strconv.FormatUint(id, 10) {
		return "", errors.New("MCP response ID does not match request")
	}
	if response.Error != nil && len(response.Result) > 0 {
		return "", errors.New("MCP response contains both result and error")
	}
	if response.Error != nil {
		return "", fmt.Errorf("MCP error %d: %s", response.Error.Code, response.Error.Message)
	}
	if len(response.Result) == 0 || json.Unmarshal(response.Result, destination) != nil {
		return "", errors.New("invalid MCP result")
	}
	return session, nil
}

func validSessionID(value string) bool {
	if len(value) > 1024 {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x21 || value[index] > 0x7e {
			return false
		}
	}
	return value != ""
}

func (c *Client) notifyLocked(ctx context.Context, method string) error {
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if err != nil {
		return err
	}
	request, err := c.request(ctx, payload)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted && (response.StatusCode < 200 || response.StatusCode >= 300) {
		return fmt.Errorf("MCP notification returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (c *Client) post(ctx context.Context, payload []byte) (rpcResponse, string, error) {
	request, err := c.request(ctx, payload)
	if err != nil {
		return rpcResponse{}, "", err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return rpcResponse{}, "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return rpcResponse{}, "", fmt.Errorf("MCP server returned HTTP %d", response.StatusCode)
	}
	var result rpcResponse
	contentType := response.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "text/event-stream") {
		result, err = decodeSSE(response.Body)
	} else {
		err = decodeBoundedJSON(response.Body, &result)
	}
	return result, response.Header.Get("Mcp-Session-Id"), err
}

func (c *Client) request(ctx context.Context, payload []byte) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	if c.bearer != "" {
		request.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	if c.session != "" {
		request.Header.Set("Mcp-Session-Id", c.session)
	}
	return request, nil
}

func decodeBoundedJSON(reader io.Reader, destination any) error {
	payload, err := io.ReadAll(io.LimitReader(reader, maxBodyBytes+1))
	if err != nil || len(payload) > maxBodyBytes {
		return errors.New("MCP response exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("MCP response contains trailing data")
	}
	return nil
}

func decodeSSE(reader io.Reader) (rpcResponse, error) {
	scanner := bufio.NewScanner(io.LimitReader(reader, maxBodyBytes+1))
	scanner.Buffer(make([]byte, 64<<10), maxBodyBytes)
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		if line == "" && data.Len() > 0 {
			var response rpcResponse
			if json.Unmarshal([]byte(data.String()), &response) == nil && len(response.ID) > 0 {
				return response, nil
			}
			data.Reset()
		}
	}
	if err := scanner.Err(); err != nil {
		return rpcResponse{}, err
	}
	return rpcResponse{}, errors.New("MCP SSE response ended without a JSON-RPC response")
}
