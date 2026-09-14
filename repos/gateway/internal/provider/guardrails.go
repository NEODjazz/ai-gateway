package provider

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync/atomic"
)

type GuardrailPolicy struct {
	Name               string   `json:"name"`
	Description        string   `json:"description,omitempty"`
	DLP                bool     `json:"dlp"`
	OutputDLP          bool     `json:"output_dlp"`
	AV                 bool     `json:"av"`
	Anonymization      string   `json:"anonymization,omitempty"`
	AnonymizationRules []string `json:"anonymization_rules,omitempty"`
	Enabled            bool     `json:"enabled"`
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

const EndpointPolicyAttachmentsMetadataKey = "policy.guardrail.endpoint_attachments"

type EndpointPolicyAttachment struct {
	PolicyName         string   `json:"policy_name"`
	Models             []string `json:"models,omitempty"`
	Providers          []string `json:"providers,omitempty"`
	Deployments        []string `json:"deployments,omitempty"`
	DLP                bool     `json:"dlp,omitempty"`
	OutputDLP          bool     `json:"output_dlp,omitempty"`
	AV                 bool     `json:"av,omitempty"`
	Anonymization      string   `json:"anonymization,omitempty"`
	AnonymizationRules []string `json:"anonymization_rules,omitempty"`
}

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
		result = append(result, cloneGuardrailPolicy(policy))
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
	return cloneGuardrailPolicy(policy), ok
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
			next[key] = cloneGuardrailPolicy(value)
		}
	}
	next[name] = cloneGuardrailPolicy(policy)
	r.guardrails.current.Store(&next)
	return cloneGuardrailPolicy(policy), nil
}

func cloneGuardrailPolicy(policy GuardrailPolicy) GuardrailPolicy {
	policy.AnonymizationRules = append([]string(nil), policy.AnonymizationRules...)
	return policy
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
	policy.Anonymization = strings.ToLower(strings.TrimSpace(policy.Anonymization))
	if !validAnonymizationRuleNames(policy.AnonymizationRules) {
		return GuardrailPolicy{}, ErrInvalidGuardrailPolicy
	}
	policy.AnonymizationRules = normalizedAnonymizationRules(policy.AnonymizationRules)
	if name == "" || len(name) > 128 || len(policy.Description) > 1024 || (!policy.DLP && !policy.AV && policy.Anonymization == "") || (policy.OutputDLP && !policy.DLP) || !validAnonymizationPolicy(policy) {
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

func validAnonymizationRuleNames(values []string) bool {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 {
			return false
		}
		for _, character := range value {
			if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') && character != '_' && character != '-' && character != '.' {
				return false
			}
		}
	}
	return true
}

func validAnonymizationPolicy(policy GuardrailPolicy) bool {
	switch policy.Anonymization {
	case "":
		return len(policy.AnonymizationRules) == 0
	case "disabled", "basic", "strict":
		return len(policy.AnonymizationRules) == 0
	case "custom":
		return len(policy.AnonymizationRules) > 0
	default:
		return false
	}
}

func normalizedAnonymizationRules(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 {
			continue
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

type AnonymizationSetting struct {
	Profile string
	Mode    string
	Rules   []string
}

func basicAnonymizationRules() []string {
	return []string{
		"address_ru", "api_key", "bank_card", "email", "inn", "jwt", "passport_ru",
		"phone", "person_ru", "secret",
	}
}

// ResolveAnonymization composes every matching profile. Strict wins over
// selected rule sets, and disabled applies only when no enabled profile asks
// for anonymization. With no explicit profile the safe default is strict.
func ResolveAnonymization(settings ...AnonymizationSetting) (mode string, rules, profiles []string) {
	explicit := false
	strict := false
	selected := make([]string, 0)
	for _, setting := range settings {
		setting.Mode = strings.ToLower(strings.TrimSpace(setting.Mode))
		if setting.Mode == "" {
			continue
		}
		explicit = true
		profiles = append(profiles, strings.Split(setting.Profile, ",")...)
		switch setting.Mode {
		case "strict":
			strict = true
		case "basic":
			selected = append(selected, basicAnonymizationRules()...)
		case "custom":
			selected = append(selected, setting.Rules...)
		}
	}
	profiles = normalizedAnonymizationRules(profiles)
	if strict || !explicit {
		return "strict", nil, profiles
	}
	rules = normalizedAnonymizationRules(selected)
	if len(rules) != 0 {
		return "custom", rules, profiles
	}
	return "disabled", nil, profiles
}

func endpointPolicySettings(metadata map[string]string, endpoint Endpoint, model string) (dlp, outputDLP, av bool, settings []AnonymizationSetting, names []string) {
	raw := strings.TrimSpace(metadata[EndpointPolicyAttachmentsMetadataKey])
	if raw == "" {
		return false, false, false, nil, nil
	}
	var attachments []EndpointPolicyAttachment
	if json.Unmarshal([]byte(raw), &attachments) != nil {
		return false, false, false, nil, nil
	}
	for _, attachment := range attachments {
		if len(attachment.Models) != 0 && !matchesEndpointPolicyPattern(model, attachment.Models) {
			continue
		}
		if len(attachment.Providers) != 0 && !matchesEndpointPolicyPattern(endpoint.ProviderID, attachment.Providers) {
			continue
		}
		if len(attachment.Deployments) != 0 && !matchesEndpointPolicyPattern(endpoint.Name, attachment.Deployments) {
			continue
		}
		dlp = dlp || attachment.DLP
		outputDLP = outputDLP || attachment.OutputDLP
		av = av || attachment.AV
		names = append(names, attachment.PolicyName)
		if attachment.Anonymization != "" {
			settings = append(settings, AnonymizationSetting{Profile: attachment.PolicyName, Mode: attachment.Anonymization, Rules: attachment.AnonymizationRules})
		}
	}
	return dlp, outputDLP, av, settings, normalizedAnonymizationRules(names)
}

func matchesEndpointPolicyPattern(value string, patterns []string) bool {
	for _, pattern := range patterns {
		if pattern == "*" || pattern == value || (strings.HasSuffix(pattern, "*") && strings.HasPrefix(value, strings.TrimSuffix(pattern, "*"))) {
			return true
		}
	}
	return false
}
