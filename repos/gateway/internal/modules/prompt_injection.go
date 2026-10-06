package modules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ai-gateway-gateway/internal/promptinjection"
)

type PromptInjectionPolicy struct {
	Name   string
	Config promptinjection.Config
}
type PromptInjectionPolicies func(*RequestContext) ([]PromptInjectionPolicy, error)
type PromptInjectionJudge func(context.Context, *RequestContext, promptinjection.Config, string) (string, error)

// PromptInjectionObserver receives metadata and an outcome, never scanned content.
type PromptInjectionObserver func(context.Context, *RequestContext, string, string, time.Duration)

type promptInjectionModule struct {
	policies PromptInjectionPolicies
	judge    PromptInjectionJudge
	observe  PromptInjectionObserver
}

func NewPromptInjectionModule(policies PromptInjectionPolicies, judge PromptInjectionJudge, observe PromptInjectionObserver) Module {
	return promptInjectionModule{policies: policies, judge: judge, observe: observe}
}
func (promptInjectionModule) Name() string   { return "prompt_injection" }
func (promptInjectionModule) Required() bool { return true }
func (m promptInjectionModule) Handle(ctx context.Context, req *RequestContext) error {
	policies, err := m.policies(req)
	if err != nil {
		return fmt.Errorf("prompt injection policy unavailable: %w", ErrGuardrailUnavailable)
	}
	if len(policies) == 0 {
		return nil
	}
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	encoded, err := json.Marshal(policies)
	if err != nil {
		return ErrGuardrailUnavailable
	}
	hash := sha256.Sum256(encoded)
	req.Metadata["provider.guardrail.prompt_injection.fingerprint"] = hex.EncodeToString(hash[:])
	for _, policy := range policies {
		started := time.Now()
		cfg, err := promptinjection.Normalize(policy.Config)
		if err != nil {
			return ErrGuardrailUnavailable
		}
		scanCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
		text, unscannable, err := PromptInjectionInput(scanCtx, req, cfg.MaxInputBytes)
		if err == nil {
			var judge promptinjection.Judge
			var accountingError error
			if m.judge != nil {
				judge = func(c context.Context, config promptinjection.Config, input string) (string, error) {
					verdict, err := m.judge(c, req, config, input)
					if errors.Is(err, promptinjection.ErrJudgeAccounting) {
						accountingError = err
					}
					return verdict, err
				}
			}
			err = promptinjection.Evaluate(scanCtx, cfg, text, unscannable, judge)
			if accountingError != nil {
				err = accountingError
			}
		}
		cancel()
		outcome := "passed"
		if errors.Is(err, promptinjection.ErrRejected) || errors.Is(err, promptinjection.ErrUnscannable) {
			outcome = "rejected"
		} else if err != nil {
			outcome = "unavailable"
		}
		req.Metadata["guardrail.prompt_injection.outcome"] = outcome
		if m.observe != nil {
			m.observe(ctx, req, policy.Name, outcome, time.Since(started))
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if outcome == "rejected" {
			return fmt.Errorf("%w: %w", ErrContentRejected, err)
		}
		if err != nil && (cfg.FailsClosed() || errors.Is(err, promptinjection.ErrJudgeAccounting)) {
			return fmt.Errorf("prompt injection check unavailable: %w", ErrGuardrailUnavailable)
		}
	}
	return nil
}
