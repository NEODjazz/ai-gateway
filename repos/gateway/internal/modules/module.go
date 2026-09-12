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
	APIKey                     string                             `json:"-"`
	RequestID                  string                             `json:"request_id,omitempty"`
	SessionID                  string                             `json:"session_id,omitempty"`
	CredentialID               string                             `json:"credential_id,omitempty"`
	CredentialAlias            string                             `json:"credential_alias,omitempty"`
	InputCharacters            int                                `json:"input_characters,omitempty"`
	InputPages                 int                                `json:"input_pages,omitempty"`
	InputAudioMilliseconds     int                                `json:"input_audio_milliseconds,omitempty"`
	VideoSeconds               int                                `json:"video_seconds,omitempty"`
	VideoProviderCostUSDTicks  *int64                             `json:"-"`
	ToolRequests               int                                `json:"tool_requests,omitempty"`
	TrainingTokens             int                                `json:"training_tokens,omitempty"`
	TeamID                     string                             `json:"team_id,omitempty"`
	OrganizationID             string                             `json:"organization_id,omitempty"`
	Tags                       []string                           `json:"tags,omitempty"`
	AccessGroupIDs             []string                           `json:"access_group_ids,omitempty"`
	AccessGroupModels          []string                           `json:"-"`
	AccessGroupTools           []string                           `json:"-"`
	AccessGroupsEvaluated      bool                               `json:"-"`
	AllowedModels              []string                           `json:"allowed_models,omitempty"`
	AllowedFallbackModels      []string                           `json:"-"`
	FallbackPolicyEvaluated    bool                               `json:"-"`
	AllowedTools               []string                           `json:"allowed_tools,omitempty"`
	RateLimitRPM               int                                `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM               int                                `json:"rate_limit_tpm,omitempty"`
	UserID                     string                             `json:"user_id,omitempty"`
	Roles                      []string                           `json:"roles,omitempty"`
	Request                    openai.ChatCompletionRequest       `json:"request"`
	CompletionRequest          *openai.CompletionRequest          `json:"completion_request,omitempty"`
	ResponseRequest            *openai.ResponseRequest            `json:"response_request,omitempty"`
	EmbeddingRequest           *openai.EmbeddingRequest           `json:"embedding_request,omitempty"`
	RerankRequest              *openai.RerankRequest              `json:"rerank_request,omitempty"`
	ModerationRequest          *openai.ModerationRequest          `json:"moderation_request,omitempty"`
	Response                   *openai.ChatCompletionResponse     `json:"response,omitempty"`
	CompletionResponse         *openai.CompletionResponse         `json:"completion_response,omitempty"`
	ResponsesResponse          *openai.ResponseResponse           `json:"responses_response,omitempty"`
	CompactedResponse          *openai.CompactedResponse          `json:"compacted_response,omitempty"`
	EmbeddingResponse          *openai.EmbeddingResponse          `json:"embedding_response,omitempty"`
	RerankResponse             *openai.RerankResponse             `json:"rerank_response,omitempty"`
	ModerationResponse         *openai.ModerationResponse         `json:"moderation_response,omitempty"`
	ImageGenerationRequest     *openai.ImageGenerationRequest     `json:"image_generation_request,omitempty"`
	ImageEditRequest           *openai.ImageEditRequest           `json:"image_edit_request,omitempty"`
	ImageVariationRequest      *openai.ImageVariationRequest      `json:"image_variation_request,omitempty"`
	ImageGenerationResponse    *openai.ImageGenerationResponse    `json:"image_generation_response,omitempty"`
	AudioTranscriptionRequest  *openai.AudioTranscriptionRequest  `json:"audio_transcription_request,omitempty"`
	AudioTranscriptionResponse *openai.AudioTranscriptionResponse `json:"audio_transcription_response,omitempty"`
	AudioSpeechRequest         *openai.AudioSpeechRequest         `json:"audio_speech_request,omitempty"`
	AudioSpeechResponse        *openai.AudioSpeechResponse        `json:"-"`
	SearchRequest              *openai.SearchRequest              `json:"search_request,omitempty"`
	SearchResponse             *openai.SearchResponse             `json:"search_response,omitempty"`
	OCRRequest                 *openai.OCRRequest                 `json:"ocr_request,omitempty"`
	OCRResponse                *openai.OCRResponse                `json:"ocr_response,omitempty"`
	SandboxRequest             *openai.SandboxExecuteRequest      `json:"sandbox_request,omitempty"`
	Usage                      *openai.Usage                      `json:"usage,omitempty"`
	Metadata                   map[string]string                  `json:"metadata,omitempty"`
	// Attachments carries validated transport-specific binary input to policy
	// modules without adding it to provider request payloads.
	Attachments         []openai.ImageAttachment `json:"-"`
	AnonymizationValues map[string]string        `json:"anonymization_values,omitempty"`
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

func (p Pipeline) HasModule(name string) bool {
	for _, module := range p.modules {
		if module.Name() == name {
			return true
		}
	}
	return false
}

func (p Pipeline) Run(ctx context.Context, req *RequestContext) error {
	return p.runPre(ctx, req, false)
}

// RunAfterAuthentication applies inference policy and billing after an owned
// resource has been resolved from an authenticated identity.
func (p Pipeline) RunAfterAuthentication(ctx context.Context, req *RequestContext) error {
	for _, module := range p.modules {
		if module.Name() == "auth" {
			continue
		}
		err := p.run(ctx, req, module, "pre", module.Handle)
		if err != nil {
			if module.Required() || errors.Is(err, ErrContentRejected) || errors.Is(err, ErrGuardrailUnavailable) {
				return fmt.Errorf("%s module failed: %w", module.Name(), err)
			}
			log.Printf("optional module %s skipped after error: %v", module.Name(), err)
		}
	}
	return nil
}

// RunTokenCount applies the configured pre-inference policies without opening
// the generation billing lifecycle. Counting never runs post/failure billing.
func (p Pipeline) RunTokenCount(ctx context.Context, req *RequestContext) error {
	return p.runPre(ctx, req, true)
}

// RunTokenCountAfterAuthentication applies counting policies to an owned
// resource resolved after authentication without running auth or billing twice.
func (p Pipeline) RunTokenCountAfterAuthentication(ctx context.Context, req *RequestContext) error {
	for _, module := range p.modules {
		if module.Name() == "auth" || module.Name() == "billing" {
			continue
		}
		err := p.run(ctx, req, module, "pre", module.Handle)
		if err != nil {
			if module.Required() || errors.Is(err, ErrContentRejected) || errors.Is(err, ErrGuardrailUnavailable) {
				return fmt.Errorf("%s module failed: %w", module.Name(), err)
			}
			log.Printf("optional module %s skipped after error: %v", module.Name(), err)
		}
	}
	return nil
}

// RunAuthentication establishes the caller identity for a non-inference
// operation without invoking content transforms or the billing lifecycle.
func (p Pipeline) RunAuthentication(ctx context.Context, req *RequestContext) error {
	found := false
	for _, module := range p.modules {
		if module.Name() != "auth" {
			continue
		}
		found = true
		err := p.run(ctx, req, module, "pre", module.Handle)
		if err != nil {
			if module.Required() {
				return fmt.Errorf("%s module failed: %w", module.Name(), err)
			}
			log.Printf("optional module %s skipped after error: %v", module.Name(), err)
		}
	}
	if !found || req.CredentialID == "" {
		return ErrUnauthorized
	}
	return nil
}

// RunBillingLifecycle executes only the billing module for non-inference
// operations that authenticate separately and must not pass through content
// transforms. The phase uses the same reserve/commit/cancel contract as model
// requests.
func (p Pipeline) RunBillingLifecycle(ctx context.Context, req *RequestContext, phase string, cause error) error {
	for _, module := range p.modules {
		if module.Name() != "billing" {
			continue
		}
		var handle func(context.Context, *RequestContext) error
		switch phase {
		case "reserve":
			handle = module.Handle
		case "commit":
			post, ok := module.(PostResponseModule)
			if !ok || !post.PostResponseEnabled() {
				return errors.New("billing module does not support commit")
			}
			handle = post.HandlePostResponse
		case "cancel":
			failure, ok := module.(FailureModule)
			if !ok {
				return errors.New("billing module does not support cancel")
			}
			handle = func(ctx context.Context, req *RequestContext) error { return failure.HandleFailure(ctx, req, cause) }
		default:
			return errors.New("invalid billing lifecycle phase")
		}
		if err := p.run(ctx, req, module, phase, handle); err != nil {
			return fmt.Errorf("billing %s failed: %w", phase, err)
		}
		return nil
	}
	return nil
}

func (p Pipeline) runPre(ctx context.Context, req *RequestContext, tokenCount bool) error {
	for _, module := range p.modules {
		if tokenCount && module.Name() == "billing" {
			continue
		}
		err := p.run(ctx, req, module, "pre", module.Handle)
		if err != nil {
			if module.Required() || errors.Is(err, ErrContentRejected) || errors.Is(err, ErrGuardrailUnavailable) {
				return fmt.Errorf("%s module failed: %w", module.Name(), err)
			}
			log.Printf("optional module %s skipped after error: %v", module.Name(), err)
		}
	}
	return nil
}

func (p Pipeline) RunPostResponse(ctx context.Context, req *RequestContext) error {
	var terminal []error
	for _, module := range p.modules {
		postModule, ok := module.(PostResponseModule)
		if !ok || !postModule.PostResponseEnabled() {
			continue
		}
		err := p.run(ctx, req, module, "post", postModule.HandlePostResponse)
		if err != nil {
			if module.Required() || errors.Is(err, ErrContentRejected) || errors.Is(err, ErrGuardrailUnavailable) {
				terminal = append(terminal, fmt.Errorf("%s post-response module failed: %w", module.Name(), err))
				continue
			}
			log.Printf("optional post-response module %s skipped after error: %v", module.Name(), err)
		}
	}
	return errors.Join(terminal...)
}

// RunNamed executes one pre-response module for a transport-specific payload
// without repeating authentication or billing lifecycle modules.
func (p Pipeline) RunNamed(ctx context.Context, req *RequestContext, name string) error {
	for _, module := range p.modules {
		if module.Name() != name {
			continue
		}
		if err := p.run(ctx, req, module, "pre", module.Handle); err != nil {
			if module.Required() || errors.Is(err, ErrContentRejected) || errors.Is(err, ErrGuardrailUnavailable) {
				return fmt.Errorf("%s module failed: %w", name, err)
			}
			log.Printf("optional module %s skipped after error: %v", name, err)
		}
		return nil
	}
	return fmt.Errorf("%s module is unavailable: %w", name, ErrGuardrailUnavailable)
}

// RunNamedPostResponse executes one post-response module. Streaming transports
// use it to apply an output policy before buffered provider bytes are released
// without repeating unrelated lifecycle modules such as billing.
func (p Pipeline) RunNamedPostResponse(ctx context.Context, req *RequestContext, name string) error {
	for _, module := range p.modules {
		if module.Name() != name {
			continue
		}
		postModule, ok := module.(PostResponseModule)
		if !ok || !postModule.PostResponseEnabled() {
			return fmt.Errorf("%s post-response module is unavailable: %w", name, ErrGuardrailUnavailable)
		}
		if err := p.run(ctx, req, module, "post", postModule.HandlePostResponse); err != nil {
			if module.Required() || errors.Is(err, ErrContentRejected) || errors.Is(err, ErrGuardrailUnavailable) {
				return fmt.Errorf("%s post-response module failed: %w", name, err)
			}
			log.Printf("optional post-response module %s skipped after error: %v", name, err)
		}
		return nil
	}
	return fmt.Errorf("%s post-response module is unavailable: %w", name, ErrGuardrailUnavailable)
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
var ErrGuardrailUnavailable = errors.New("guardrail unavailable")
