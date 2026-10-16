package provider

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/promptinjection"
)

func (r *Router) promptPolicies(req *modules.RequestContext) ([]modules.PromptInjectionPolicy, error) {
	var result []modules.PromptInjectionPolicy
	for _, name := range splitMetadataList(req.Metadata["provider.guardrail.policy"]) {
		policy, found := r.GetGuardrailPolicy(name)
		if !found || !policy.Enabled {
			return nil, modules.ErrGuardrailUnavailable
		}
		if policy.PromptInjection != nil {
			result = append(result, modules.PromptInjectionPolicy{Name: name, Config: *promptinjection.Clone(policy.PromptInjection)})
		}
	}
	if len(result) > 32 {
		return nil, modules.ErrGuardrailUnavailable
	}
	return result, nil
}

func (r *Router) promptJudgeEndpoint(id string) (Endpoint, bool) {
	for _, endpoint := range r.activeEndpoints() {
		if endpoint.Name == id && !endpoint.Shadow && len(endpoint.Models) > 0 && endpoint.Provider != nil && endpoint.supportsCapabilities("chat") {
			return endpoint, true
		}
	}
	return Endpoint{}, false
}
func (r *Router) validPromptJudge(id string) bool { _, ok := r.promptJudgeEndpoint(id); return ok }

// CheckPromptInjection shares the inference detector with explicit policy tests.
// The authenticated identity is retained for classifier billing.
func (r *Router) CheckPromptInjection(ctx context.Context, req *modules.RequestContext) error {
	return modules.NewPromptInjectionModule(r.promptPolicies, r.promptJudge, r.promptObserver).Handle(ctx, req)
}

func (r *Router) providerAttemptContext(req modules.RequestContext, endpoint Endpoint) modules.RequestContext {
	attempt := providerAttemptContext(req, endpoint)
	delete(attempt.Metadata, "provider.guardrail.prompt_injection.fingerprint")
	policies, err := r.promptPolicies(&attempt)
	if err == nil && len(policies) > 0 {
		encoded, _ := json.Marshal(policies)
		hash := sha256.Sum256(encoded)
		attempt.Metadata["provider.guardrail.prompt_injection.fingerprint"] = hex.EncodeToString(hash[:])
	}
	return attempt
}

func (r *Router) promptJudge(ctx context.Context, parent *modules.RequestContext, cfg promptinjection.Config, text string) (string, error) {
	endpoint, found := r.promptJudgeEndpoint(cfg.JudgeDeploymentID)
	if !found || (endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid) {
		return "", promptinjection.ErrUnavailable
	}
	release, err := r.promptAdmission.acquire(ctx, "prompt_injection")
	if err != nil {
		return "", promptinjection.ErrUnavailable
	}
	defer release()
	// This configured deployment is a delegated policy service, not a caller-selected
	// model. Only administrators can select it. Bill it to the original caller.
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", promptinjection.ErrUnavailable
	}
	limit := 64
	payload, _ := json.Marshal(struct {
		Content string `json:"untrusted_content"`
	}{text})
	request := modules.RequestContext{
		RequestID: "guardrail-" + hex.EncodeToString(id), CredentialID: parent.CredentialID, CredentialAlias: parent.CredentialAlias,
		UserID: parent.UserID, TeamID: parent.TeamID, OrganizationID: parent.OrganizationID, Roles: append([]string(nil), parent.Roles...), Tags: append([]string(nil), parent.Tags...),
		Request:  openai.ChatCompletionRequest{Model: endpoint.Models[0], MaxTokens: &limit, Messages: []openai.Message{{Role: "system", Content: cfg.JudgeSystemPrompt}, {Role: "user", Content: string(payload)}}},
		Metadata: map[string]string{"gateway.api_type": "prompt_injection_judge", "guardrail.monitor.source": "prompt_injection_judge"},
	}
	request = providerAttemptContext(request, endpoint)
	r.applyCatalogPricing(ctx, &request, endpoint, endpoint.Models[0])
	// Preserve DLP, AV, anonymization and billing, but never recursively classify
	// classifier input. Do not retry or fall back to a different policy service.
	pipeline := r.modules.Without("prompt_injection")
	if err := pipeline.RunAfterAuthentication(ctx, &request); err != nil {
		pipeline.RunFailure(ctx, &request, err)
		return "", fmt.Errorf("%w: %w", promptinjection.ErrJudgeAccounting, err)
	}
	endpoint.MaxRetries = 0
	endpoint.RetryPolicy = nil
	started := time.Now()
	response, _, err := r.callChat(ctx, endpoint, request.Request)
	setAttemptMetadata(&request, started, err)
	if err != nil {
		pipeline.RunFailure(ctx, &request, err)
		return "", promptinjection.ErrUnavailable
	}
	if err := mergeChatUsage(&response, request.Usage); err != nil {
		pipeline.RunFailure(ctx, &request, err)
		return "", promptinjection.ErrUnavailable
	}
	request.Response = &response
	if err := pipeline.RunPostResponse(ctx, &request); err != nil {
		return "", fmt.Errorf("%w: %w", promptinjection.ErrJudgeAccounting, err)
	}
	if len(response.Choices) != 1 || len(response.Choices[0].Message.ToolCalls) != 0 {
		return "", promptinjection.ErrUnavailable
	}
	verdict := openai.ContentText(response.Choices[0].Message.Content)
	if len(verdict) > 256 {
		return "", promptinjection.ErrUnavailable
	}
	return strings.TrimSpace(verdict), nil
}
