package provider

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync/atomic"
)

type GuardrailPolicy struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	DLP         bool   `json:"dlp"`
	AV          bool   `json:"av"`
	Enabled     bool   `json:"enabled"`
}
type guardrailRegistry struct {
	current atomic.Pointer[map[string]GuardrailPolicy]
}
type GuardrailController interface {
	ListGuardrailPolicies() []GuardrailPolicy
	GetGuardrailPolicy(string) (GuardrailPolicy, bool)
	UpdateGuardrailPolicy(string, GuardrailPolicy) (GuardrailPolicy, error)
}

type DurableGuardrailController interface {
	UpdateGuardrailPolicyDurable(context.Context, string, GuardrailPolicy) (GuardrailPolicy, error)
}

var ErrInvalidGuardrailPolicy = errors.New("invalid guardrail policy")

func (r *Router) ListGuardrailPolicies() []GuardrailPolicy {
	if r == nil || r.guardrails == nil {
		return nil
	}
	current := r.guardrails.current.Load()
	if current == nil {
		return nil
	}
	result := make([]GuardrailPolicy, 0, len(*current))
	for _, policy := range *current {
		result = append(result, policy)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}
func (r *Router) GetGuardrailPolicy(name string) (GuardrailPolicy, bool) {
	if r == nil || r.guardrails == nil {
		return GuardrailPolicy{}, false
	}
	current := r.guardrails.current.Load()
	if current == nil {
		return GuardrailPolicy{}, false
	}
	policy, ok := (*current)[name]
	return policy, ok
}
func (r *Router) UpdateGuardrailPolicy(name string, policy GuardrailPolicy) (GuardrailPolicy, error) {
	policy, err := normalizeGuardrailPolicy(name, policy)
	if err != nil {
		return GuardrailPolicy{}, err
	}
	name = policy.Name
	if r.guardrails == nil {
		r.guardrails = &guardrailRegistry{}
	}
	current := r.guardrails.current.Load()
	next := map[string]GuardrailPolicy{}
	if current != nil {
		for key, value := range *current {
			next[key] = value
		}
	}
	next[name] = policy
	r.guardrails.current.Store(&next)
	return policy, nil
}

func (r *Router) UpdateGuardrailPolicyDurable(ctx context.Context, name string, policy GuardrailPolicy) (GuardrailPolicy, error) {
	previous, unlock, err := r.beginControlMutation(ctx)
	if err != nil {
		return GuardrailPolicy{}, err
	}
	defer unlock()
	saved, err := r.UpdateGuardrailPolicy(name, policy)
	if err != nil {
		return GuardrailPolicy{}, err
	}
	if err := r.persistControlMutation(ctx, previous); err != nil {
		return GuardrailPolicy{}, err
	}
	return saved, nil
}

func normalizeGuardrailPolicy(name string, policy GuardrailPolicy) (GuardrailPolicy, error) {
	name = strings.TrimSpace(name)
	policy.Description = strings.TrimSpace(policy.Description)
	if name == "" || len(name) > 128 || len(policy.Description) > 1024 || (!policy.DLP && !policy.AV) {
		return GuardrailPolicy{}, ErrInvalidGuardrailPolicy
	}
	for _, value := range name {
		if !(value >= 'a' && value <= 'z') && !(value >= 'A' && value <= 'Z') && !(value >= '0' && value <= '9') && value != '-' && value != '_' && value != '.' {
			return GuardrailPolicy{}, ErrInvalidGuardrailPolicy
		}
	}
	policy.Name = name
	return policy, nil
}
