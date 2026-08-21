package modules

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"ai-gateway-gateway/internal/openai"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

type RequestContext struct {
	APIKey              string                         `json:"-"`
	RequestID           string                         `json:"request_id,omitempty"`
	CredentialID        string                         `json:"credential_id,omitempty"`
	TeamID              string                         `json:"team_id,omitempty"`
	AllowedModels       []string                       `json:"allowed_models,omitempty"`
	AllowedTools        []string                       `json:"allowed_tools,omitempty"`
	RateLimitRPM        int                            `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM        int                            `json:"rate_limit_tpm,omitempty"`
	UserID              string                         `json:"user_id,omitempty"`
	Roles               []string                       `json:"roles,omitempty"`
	Request             openai.ChatCompletionRequest   `json:"request"`
	ResponseRequest     *openai.ResponseRequest        `json:"response_request,omitempty"`
	EmbeddingRequest    *openai.EmbeddingRequest       `json:"embedding_request,omitempty"`
	Response            *openai.ChatCompletionResponse `json:"response,omitempty"`
	ResponsesResponse   *openai.ResponseResponse       `json:"responses_response,omitempty"`
	EmbeddingResponse   *openai.EmbeddingResponse      `json:"embedding_response,omitempty"`
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
	modules  []Module
	observer ModuleObserver
}

type ModuleObserver interface {
	ObserveModule(module, phase, result string, duration time.Duration)
}

func NewPipeline(modules []Module) Pipeline {
	return Pipeline{modules: modules}
}

func NewPipelineWithObserver(modules []Module, observer ModuleObserver) Pipeline {
	return Pipeline{modules: modules, observer: observer}
}

func (p Pipeline) Run(ctx context.Context, req *RequestContext) error {
	for _, module := range p.modules {
		err := p.run(ctx, req, module, "pre", module.Handle)
		if err != nil {
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
		err := p.run(ctx, req, module, "post", postModule.HandlePostResponse)
		if err != nil {
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
		started := time.Now()
		spanCtx, span := otel.Tracer("ai-gateway/modules").Start(ctx, "module."+module.Name()+".failure")
		span.SetAttributes(attribute.String("ai.module.name", module.Name()), attribute.String("ai.module.phase", "failure"))
		err := failureModule.HandleFailure(spanCtx, req, cause)
		result := moduleResult(err)
		span.SetAttributes(attribute.String("ai.module.result", result))
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, result)
		}
		span.End()
		if p.observer != nil {
			p.observer.ObserveModule(module.Name(), "failure", result, time.Since(started))
		}
		if err != nil {
			log.Printf("failure hook %s skipped after error: %v", module.Name(), err)
		}
	}
}

func (p Pipeline) run(ctx context.Context, req *RequestContext, module Module, phase string, call func(context.Context, *RequestContext) error) error {
	started := time.Now()
	spanCtx, span := otel.Tracer("ai-gateway/modules").Start(ctx, "module."+module.Name()+"."+phase)
	span.SetAttributes(attribute.String("ai.module.name", module.Name()), attribute.String("ai.module.phase", phase))
	err := call(spanCtx, req)
	result := moduleResult(err)
	span.SetAttributes(attribute.String("ai.module.result", result))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, result)
	}
	span.End()
	if p.observer != nil {
		p.observer.ObserveModule(module.Name(), phase, result, time.Since(started))
	}
	return err
}

func moduleResult(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrContentRejected):
		return "content_rejected"
	case errors.Is(err, ErrBudgetExceeded):
		return "budget_exceeded"
	case errors.Is(err, ErrBillingConflict):
		return "billing_conflict"
	case errors.Is(err, ErrUnauthorized):
		return "unauthorized"
	default:
		return "error"
	}
}

var ErrUnauthorized = errors.New("unauthorized")
var ErrBudgetExceeded = errors.New("budget exceeded")
var ErrBillingConflict = errors.New("billing lifecycle conflict")
var ErrContentRejected = errors.New("content rejected")
