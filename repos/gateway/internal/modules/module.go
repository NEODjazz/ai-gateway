package modules

import (
	"context"
	"errors"
	"fmt"
	"log"

	"ai-gateway-gateway/internal/openai"
)

type RequestContext struct {
	APIKey              string                         `json:"-"`
	RequestID           string                         `json:"request_id,omitempty"`
	CredentialID        string                         `json:"credential_id,omitempty"`
	TeamID              string                         `json:"team_id,omitempty"`
	AllowedModels       []string                       `json:"allowed_models,omitempty"`
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

type PostResponseModule interface {
	Module
	PostResponseEnabled() bool
	HandlePostResponse(ctx context.Context, req *RequestContext) error
}

type FailureModule interface {
	Module
	HandleFailure(ctx context.Context, req *RequestContext, cause error) error
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
			if module.Required() || errors.Is(err, ErrContentRejected) {
				return fmt.Errorf("%s module failed: %w", module.Name(), err)
			}
			log.Printf("optional module %s skipped after error: %v", module.Name(), err)
		}
	}
	return nil
}

func (p Pipeline) RunPostResponse(ctx context.Context, req *RequestContext) error {
	for _, module := range p.modules {
		postModule, ok := module.(PostResponseModule)
		if !ok || !postModule.PostResponseEnabled() {
			continue
		}
		if err := postModule.HandlePostResponse(ctx, req); err != nil {
			if module.Required() || errors.Is(err, ErrContentRejected) {
				return fmt.Errorf("%s post-response module failed: %w", module.Name(), err)
			}
			log.Printf("optional post-response module %s skipped after error: %v", module.Name(), err)
		}
	}
	return nil
}

func (p Pipeline) RunFailure(ctx context.Context, req *RequestContext, cause error) {
	for _, module := range p.modules {
		failureModule, ok := module.(FailureModule)
		if !ok {
			continue
		}
		if err := failureModule.HandleFailure(ctx, req, cause); err != nil {
			log.Printf("failure hook %s skipped after error: %v", module.Name(), err)
		}
	}
}

var ErrUnauthorized = errors.New("unauthorized")
var ErrBudgetExceeded = errors.New("budget exceeded")
var ErrBillingConflict = errors.New("billing lifecycle conflict")
var ErrContentRejected = errors.New("content rejected")
