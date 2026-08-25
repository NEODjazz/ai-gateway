package modules

import (
	"context"
	"errors"
	"fmt"
	"log"

	"ai-gateway-anonymizer/internal/openai"
)

type RequestContext struct {
	UserID              string                         `json:"user_id,omitempty"`
	Roles               []string                       `json:"roles,omitempty"`
	Request             openai.ChatCompletionRequest   `json:"request"`
	ResponseRequest     *openai.ResponseRequest        `json:"response_request,omitempty"`
	RerankRequest       *openai.RerankRequest          `json:"rerank_request,omitempty"`
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
