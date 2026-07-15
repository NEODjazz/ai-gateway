package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type failingProvider struct{}

func (failingProvider) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, errors.New("provider is down")
}

func (failingProvider) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, errors.New("provider is down")
}

type staticProvider struct {
	content string
}

func (p staticProvider) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{
		Model: "test-model",
		Choices: []openai.Choice{
			{Message: openai.Message{Role: "assistant", Content: p.content}},
		},
	}, nil
}

type echoProvider struct {
	seen string
}

type metadataModule struct {
	key  string
	seen []string
}

func (m *metadataModule) Name() string {
	return "metadata"
}

func (m *metadataModule) Required() bool {
	return true
}

func (m *metadataModule) Handle(_ context.Context, req *modules.RequestContext) error {
	m.seen = append(m.seen, req.Metadata[m.key])
	return nil
}

func (p *echoProvider) ChatCompletions(_ context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if len(request.Messages) > 0 {
		p.seen = openai.ContentText(request.Messages[len(request.Messages)-1].Content)
	}
	return openai.ChatCompletionResponse{
		Model: request.Model,
		Choices: []openai.Choice{
			{Message: openai.Message{Role: "assistant", Content: p.seen}},
		},
	}, nil
}

func (p *echoProvider) Responses(_ context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	if value, ok := request.Input.(string); ok {
		p.seen = value
	}
	return openai.ResponseResponse{
		Model:      request.Model,
		OutputText: p.seen,
		Output: []openai.ResponseOutputItem{
			{Type: "message", Content: []openai.ResponseOutputContent{{Type: "output_text", Text: p.seen}}},
		},
	}, nil
}

func (p staticProvider) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{
		Model:      "test-model",
		OutputText: p.content,
		Output: []openai.ResponseOutputItem{
			{Type: "message", Content: []openai.ResponseOutputContent{{Type: "output_text", Text: p.content}}},
		},
	}, nil
}

func TestRouterFallsBackToNextEndpoint(t *testing.T) {
	router := Router{
		defaultProvider: "ollama",
		endpoints: []Endpoint{
			{
				Name:     "primary",
				Type:     "ollama",
				Models:   []string{"test-model"},
				Priority: 10,
				Provider: failingProvider{},
			},
			{
				Name:     "secondary",
				Type:     "ollama",
				Models:   []string{"test-model"},
				Priority: 20,
				Provider: staticProvider{content: "fallback ok"},
			},
		},
	}

	response, err := router.ChatCompletions(context.Background(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{
			Provider: "ollama",
			Model:    "test-model",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "fallback ok" {
		t.Fatalf("unexpected fallback response: %s", openai.ContentText(response.Choices[0].Message.Content))
	}
}

func TestRouterFallsBackToNextResponsesEndpoint(t *testing.T) {
	router := Router{
		defaultProvider: "ollama",
		endpoints: []Endpoint{
			{Name: "primary", Type: "ollama", Models: []string{"test-model"}, Priority: 10, Provider: failingProvider{}},
			{Name: "secondary", Type: "ollama", Models: []string{"test-model"}, Priority: 20, Provider: staticProvider{content: "responses fallback ok"}},
		},
	}

	response, err := router.Responses(context.Background(), modules.RequestContext{
		ResponseRequest: &openai.ResponseRequest{
			Provider: "ollama",
			Model:    "test-model",
			Input:    "hello",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.OutputText != "responses fallback ok" {
		t.Fatalf("unexpected fallback response: %s", response.OutputText)
	}
}

func TestRouterAppliesProviderLevelModulesForChat(t *testing.T) {
	echo := &echoProvider{}
	router := Router{
		defaultProvider: "demo",
		modules: modules.NewPipeline([]modules.Module{
			modules.NewAnonymizerModule(true, modules.RuleEmail),
		}),
		endpoints: []Endpoint{
			{Name: "echo", Type: "demo", Provider: echo},
		},
	}

	response, err := router.ChatCompletions(context.Background(), modules.RequestContext{
		UserID: "user-1",
		Roles:  []string{"developer"},
		Request: openai.ChatCompletionRequest{
			Model: "test-model",
			Messages: []openai.Message{
				{Role: "user", Content: "user@example.com"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if echo.seen != "{{EMAIL_1}}" {
		t.Fatalf("expected provider to receive anonymized input, got %q", echo.seen)
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "user@example.com" {
		t.Fatalf("expected client response to be deanonymized, got %q", response.Choices[0].Message.Content)
	}
}

func TestRouterAppliesProviderLevelModulesForResponses(t *testing.T) {
	echo := &echoProvider{}
	router := Router{
		defaultProvider: "demo",
		modules: modules.NewPipeline([]modules.Module{
			modules.NewAnonymizerModule(true, modules.RuleEmail),
		}),
		endpoints: []Endpoint{
			{Name: "echo", Type: "demo", Provider: echo},
		},
	}

	response, err := router.Responses(context.Background(), modules.RequestContext{
		UserID: "user-1",
		Roles:  []string{"developer"},
		ResponseRequest: &openai.ResponseRequest{
			Model: "test-model",
			Input: "user@example.com",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if echo.seen != "{{EMAIL_1}}" {
		t.Fatalf("expected provider to receive anonymized input, got %q", echo.seen)
	}
	if !strings.Contains(response.OutputText, "user@example.com") {
		t.Fatalf("expected client response to be deanonymized, got %q", response.OutputText)
	}
}

func TestRouterAddsProviderModuleFlagsToAttemptContext(t *testing.T) {
	dlpModule := &metadataModule{key: "provider.modules.dlp.enabled"}
	avModule := &metadataModule{key: "provider.modules.av.enabled"}
	router := New(Config{
		Default: "auto",
		Modules: modules.NewPipeline([]modules.Module{
			dlpModule,
			avModule,
		}),
		Endpoints: []config.ProviderEndpointConfig{
			{Name: "checked", Type: "demo", Models: []string{"checked-model"}, DLPEnabled: true, AVEnabled: true, Enabled: testBoolPtr(true)},
			{Name: "plain", Type: "demo", Models: []string{"plain-model"}, Enabled: testBoolPtr(true)},
		},
	})

	_, err := router.ChatCompletions(context.Background(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{
			Model: "checked-model",
			Messages: []openai.Message{
				{Role: "user", Content: "hello"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dlpModule.seen[0] != "true" || avModule.seen[0] != "true" {
		t.Fatalf("expected enabled flags for checked provider, got dlp=%q av=%q", dlpModule.seen[0], avModule.seen[0])
	}

	_, err = router.ChatCompletions(context.Background(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{
			Model: "plain-model",
			Messages: []openai.Message{
				{Role: "user", Content: "hello"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dlpModule.seen[1] != "false" || avModule.seen[1] != "false" {
		t.Fatalf("expected disabled flags for plain provider, got dlp=%q av=%q", dlpModule.seen[1], avModule.seen[1])
	}
}

func TestRouterFiltersByProviderAndModel(t *testing.T) {
	router := New(Config{
		Default: "auto",
		Endpoints: []config.ProviderEndpointConfig{
			{
				Name:    "demo-a",
				Type:    "demo",
				Models:  []string{"model-a"},
				Enabled: testBoolPtr(true),
			},
		},
	})

	_, err := router.ChatCompletions(context.Background(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{
			Provider: "demo",
			Model:    "model-b",
		},
	})
	if err == nil {
		t.Fatal("expected no provider endpoint error")
	}
}

func TestRouterDoesNotFallbackToDemoForUnsupportedDefaultProviderModel(t *testing.T) {
	router := Router{
		defaultProvider: "ollama",
		endpoints: []Endpoint{
			{Name: "ollama-local", Type: "ollama", Models: []string{"lfm2.5-thinking:1.2b"}, Provider: staticProvider{content: "ollama"}},
			{Name: "demo", Type: "demo", Models: []string{"fallback-demo-model"}, Provider: staticProvider{content: "demo"}},
		},
	}

	_, err := router.Responses(context.Background(), modules.RequestContext{
		ResponseRequest: &openai.ResponseRequest{
			Model: "lfm2.5-thinking:2b",
			Input: "test",
		},
	})
	if err == nil {
		t.Fatal("expected unsupported model error")
	}
	if !strings.Contains(err.Error(), `model="lfm2.5-thinking:2b"`) {
		t.Fatalf("expected model in error, got %v", err)
	}
}

func TestRouterRoutesByModelWhenProviderIsOmitted(t *testing.T) {
	router := Router{
		defaultProvider: "ollama",
		endpoints: []Endpoint{
			{Name: "ollama-local", Type: "ollama", Models: []string{"lfm2.5-thinking:1.2b"}, Provider: staticProvider{content: "ollama"}},
			{Name: "azure-open-ai", Type: "openai", Models: []string{"gpt-5.3"}, Provider: staticProvider{content: "azure"}},
		},
	}

	response, err := router.ChatCompletions(context.Background(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{
			Model: "gpt-5.3",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "azure" {
		t.Fatalf("expected azure provider, got %q", response.Choices[0].Message.Content)
	}
}

func TestRouterModelsReturnsConfiguredModels(t *testing.T) {
	router := Router{
		endpoints: []Endpoint{
			{Name: "ollama-local", Type: "ollama", Models: []string{"qwen3.5:9b", "lfm2.5-thinking:1.2b"}},
			{Name: "azure-open-ai", Type: "openai", Models: []string{"gpt-5.3"}},
			{Name: "demo", Type: "demo"},
		},
	}

	models := router.Models()
	if len(models) != 4 {
		t.Fatalf("expected 4 models, got %d: %+v", len(models), models)
	}
	expected := []string{"demo", "gpt-5.3", "lfm2.5-thinking:1.2b", "qwen3.5:9b"}
	for index, model := range models {
		if model.ID != expected[index] {
			t.Fatalf("expected model %q at index %d, got %q", expected[index], index, model.ID)
		}
		if model.Object != "model" {
			t.Fatalf("expected model object, got %q", model.Object)
		}
	}
}

func testBoolPtr(value bool) *bool {
	return &value
}
