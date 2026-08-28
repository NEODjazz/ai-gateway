package modules

import (
	"context"
	"errors"
	"fmt"
	"log"

	"ai-gateway-auth/internal/openai"
)

type RequestContext struct {
	APIKey              string                         `json:"api_key,omitempty"`
	CredentialID        string                         `json:"credential_id,omitempty"`
	TeamID              string                         `json:"team_id,omitempty"`
	OrganizationID      string                         `json:"organization_id,omitempty"`
	AllowedModels       []string                       `json:"allowed_models,omitempty"`
	AllowedTools        []string                       `json:"allowed_tools,omitempty"`
	RateLimitRPM        int                            `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM        int                            `json:"rate_limit_tpm,omitempty"`
	UserID              string                         `json:"user_id,omitempty"`
	Roles               []string                       `json:"roles,omitempty"`
	Request             openai.ChatCompletionRequest   `json:"request"`
	ResponseRequest     *openai.ResponseRequest        `json:"response_request,omitempty"`
	Response            *openai.ChatCompletionResponse `json:"response,omitempty"`
	ResponsesResponse   *openai.ResponseResponse       `json:"responses_response,omitempty"`
	Usage               *openai.Usage                  `json:"usage,omitempty"`
	Metadata            map[string]string              `json:"metadata,omitempty"`
	AnonymizationValues map[string]string              `json:"anonymization_values,omitempty"`
}

type Module interface {
	Name() string
	Required() bool
	Handle(ctx context.Context, req *RequestContext) error
}

type Pipeline struct {
	modules []Module
}

func NewPipeline(modules []Module) Pipeline {
	return Pipeline{modules: modules}
}

func (p Pipeline) Run(ctx context.Context, req *RequestContext) error {
	for _, module := range p.modules {
		if err := module.Handle(ctx, req); err != nil {
			if module.Required() {
				return fmt.Errorf("%s module failed: %w", module.Name(), err)
			}
			log.Printf("optional module %s skipped after error: %v", module.Name(), err)
		}
	}
	return nil
}

var ErrUnauthorized = errors.New("unauthorized")
