package modules

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

type ProviderRemoteModule struct {
	name        string
	required    bool
	metadataKey string
	endpoint    string
	client      *http.Client
}

func NewProviderRemoteModule(name string, required bool, endpoint string) ProviderRemoteModule {
	return ProviderRemoteModule{
		name:        name,
		required:    required,
		metadataKey: "provider.modules." + name + ".enabled",
		endpoint:    endpoint,
		client:      newRemoteHTTPClient(),
	}
}

func (m ProviderRemoteModule) Name() string {
	return m.name
}

func (m ProviderRemoteModule) Required() bool {
	return m.required
}

func (m ProviderRemoteModule) Handle(ctx context.Context, req *RequestContext) error {
	if !metadataBool(req.Metadata, m.metadataKey) {
		return nil
	}
	if strings.TrimSpace(m.endpoint) == "" {
		return errors.New("remote module url is empty")
	}
	_, err := callRemote[ScanRequest, ScanResponse](ctx, m.client, m.endpoint, ScanRequest{
		RequestID: req.RequestID,
		Content:   scanPayload(req),
	})
	return err
}

type ScanRequest struct {
	RequestID string `json:"request_id,omitempty"`
	Content   string `json:"content"`
}

type ScanResponse struct {
	Allowed bool `json:"allowed"`
}

func scanPayload(req *RequestContext) string {
	var parts []string
	for _, message := range req.Request.Messages {
		if text := openai.ContentText(message.Content); text != "" {
			parts = append(parts, message.Role+": "+text)
		}
		for _, call := range message.ToolCalls {
			if call.Function.Arguments != "" {
				parts = append(parts, "tool_arguments: "+call.Function.Arguments)
			}
		}
	}
	if req.ResponseRequest != nil {
		if req.ResponseRequest.Instructions != "" {
			parts = append(parts, "instructions: "+req.ResponseRequest.Instructions)
		}
		if text := openai.ContentText(req.ResponseRequest.Input); text != "" {
			parts = append(parts, "input: "+text)
		}
	}
	return strings.Join(parts, "\n")
}

func metadataBool(metadata map[string]string, key string) bool {
	switch strings.ToLower(strings.TrimSpace(metadata[key])) {
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
}
