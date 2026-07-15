package modules

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const RuleValidatorLuhn = "luhn"

type RuleConfig struct {
	Name         string `json:"name"`
	Placeholder  string `json:"placeholder"`
	Pattern      string `json:"pattern"`
	Validator    string `json:"validator,omitempty"`
	CaptureGroup int    `json:"capture_group,omitempty"`
}

type RulesConfig struct {
	Rules []RuleConfig `json:"rules"`
}

func LoadRulesConfig(path string) ([]RuleConfig, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read anonymizer rules config: %w", err)
	}

	var config RulesConfig
	if err := json.Unmarshal(content, &config); err != nil {
		return nil, fmt.Errorf("decode anonymizer rules config: %w", err)
	}
	if len(config.Rules) == 0 {
		return nil, fmt.Errorf("anonymizer rules config contains no rules")
	}
	return config.Rules, nil
}

func NewAnonymizerModuleFromConfig(required bool, config []RuleConfig, enabledRules ...string) (AnonymizerModule, error) {
	rules, err := buildConfiguredRules(config, enabledRules)
	if err != nil {
		return AnonymizerModule{}, err
	}
	return AnonymizerModule{required: required, rules: rules}, nil
}

func buildConfiguredRules(config []RuleConfig, enabled []string) ([]AnonymizerRule, error) {
	selected := selectedRuleNames(enabled)
	disabled := selected["__disabled__"]
	known := make(map[string]bool, len(config))
	rules := make([]AnonymizerRule, 0, len(config))

	for index, item := range config {
		item.Name = strings.TrimSpace(strings.ToLower(item.Name))
		if item.Name == "" {
			return nil, fmt.Errorf("anonymizer rule %d has an empty name", index)
		}
		if known[item.Name] {
			return nil, fmt.Errorf("duplicate anonymizer rule %q", item.Name)
		}
		known[item.Name] = true
		if item.Placeholder == "" {
			return nil, fmt.Errorf("anonymizer rule %q has an empty placeholder", item.Name)
		}
		if item.Pattern == "" {
			return nil, fmt.Errorf("anonymizer rule %q has an empty pattern", item.Name)
		}

		pattern, err := regexp.Compile(item.Pattern)
		if err != nil {
			return nil, fmt.Errorf("compile anonymizer rule %q: %w", item.Name, err)
		}
		if item.CaptureGroup < 0 || item.CaptureGroup > pattern.NumSubexp() {
			return nil, fmt.Errorf("anonymizer rule %q references missing capture group %d", item.Name, item.CaptureGroup)
		}

		var shouldMask func(string) bool
		switch strings.TrimSpace(strings.ToLower(item.Validator)) {
		case "":
		case RuleValidatorLuhn:
			shouldMask = isLikelyBankCard
		default:
			return nil, fmt.Errorf("anonymizer rule %q uses unsupported validator %q", item.Name, item.Validator)
		}

		if !disabled && (len(selected) == 0 || selected["all"] || selected[item.Name]) {
			rules = append(rules, AnonymizerRule{
				Name:         item.Name,
				Placeholder:  item.Placeholder,
				Pattern:      pattern,
				ShouldMask:   shouldMask,
				CaptureGroup: item.CaptureGroup,
			})
		}
	}

	if !disabled && !selected["all"] {
		for name := range selected {
			if name != "__disabled__" && !known[name] {
				return nil, fmt.Errorf("enabled anonymizer rule %q is not defined", name)
			}
		}
	}
	return rules, nil
}

func selectedRuleNames(enabled []string) map[string]bool {
	selected := make(map[string]bool, len(enabled))
	for _, name := range enabled {
		name = strings.TrimSpace(strings.ToLower(name))
		if name != "" {
			selected[name] = true
		}
	}
	return selected
}
