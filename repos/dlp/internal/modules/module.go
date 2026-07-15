package modules

import (
	"strings"

	"ai-gateway-dlp/internal/openai"
)

type RequestContext struct {
	APIKey          string                       `json:"api_key,omitempty"`
	UserID          string                       `json:"user_id,omitempty"`
	Roles           []string                     `json:"roles,omitempty"`
	Request         openai.ChatCompletionRequest `json:"request"`
	ResponseRequest *openai.ResponseRequest      `json:"response_request,omitempty"`
	Metadata        map[string]string            `json:"metadata,omitempty"`
}

func ScanPayload(req *RequestContext) string {
	var parts []string
	for _, message := range req.Request.Messages {
		if text := openai.ContentText(message.Content); text != "" {
			parts = append(parts, message.Role+": "+text)
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
