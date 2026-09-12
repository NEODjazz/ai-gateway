package modules

import (
	"context"
	"os"
	"regexp"
	"strings"

	"ai-gateway-anonymizer/internal/openai"
)

const (
	RuleEmail      = "email"
	RulePhone      = "phone"
	RulePersonRU   = "person_ru"
	RuleAddressRU  = "address_ru"
	RulePassportRU = "passport_ru"
	RuleINN        = "inn"
	RuleBankCard   = "bank_card"
	RuleIP         = "ip"
	RuleAPIKey     = "api_key"
	RuleJWT        = "jwt"
	RuleSecret     = "secret"
)

var defaultAnonymizerRules = []string{
	RuleEmail,
	RulePhone,
	RulePersonRU,
	RuleAddressRU,
	RulePassportRU,
	RuleINN,
	RuleBankCard,
	RuleIP,
	RuleAPIKey,
	RuleJWT,
	RuleSecret,
}

type AnonymizerModule struct {
	required bool
	rules    []AnonymizerRule
}

type AnonymizerRule struct {
	Name         string
	Placeholder  string
	Pattern      *regexp.Regexp
	ShouldMask   func(value string) bool
	CaptureGroup int
}

func NewAnonymizerModule(required bool, enabledRules ...string) AnonymizerModule {
	module, err := NewAnonymizerModuleFromConfig(required, defaultRuleConfig(), enabledRules...)
	if err != nil {
		panic(err)
	}
	return module
}

func AnonymizerRulesFromEnv() []string {
	return parseAnonymizerRules(os.Getenv("ANONYMIZER_RULES"))
}

func (m AnonymizerModule) Name() string {
	return "anonymizer"
}

func (m AnonymizerModule) Required() bool {
	return m.required
}

func (m AnonymizerModule) RuleNames() []string {
	names := make([]string, 0, len(m.rules))
	for _, rule := range m.rules {
		names = append(names, rule.Name)
	}
	return names
}

func (m AnonymizerModule) Handle(_ context.Context, req *RequestContext) error {
	return m.handle(req, m.rules)
}

func (m AnonymizerModule) HandleWithRules(req *RequestContext, enabledRules []string) error {
	if len(enabledRules) == 0 {
		return m.handle(req, m.rules)
	}
	selected := selectedRuleNames(enabledRules)
	if selected["__disabled__"] {
		return nil
	}
	rules := make([]AnonymizerRule, 0, len(m.rules))
	matched := make(map[string]bool, len(selected))
	for _, rule := range m.rules {
		if selected["all"] || selected[rule.Name] {
			rules = append(rules, rule)
			matched[rule.Name] = true
		}
	}
	if !selected["all"] {
		for name := range selected {
			if !matched[name] {
				return &RuleSelectionError{Name: name}
			}
		}
	}
	return m.handle(req, rules)
}

type RuleSelectionError struct{ Name string }

func (e *RuleSelectionError) Error() string { return "unknown anonymizer rule " + e.Name }

func (m AnonymizerModule) handle(req *RequestContext, rules []AnonymizerRule) error {
	for index := range req.Request.Messages {
		req.Request.Messages[index].Content = anonymizeAny(req, req.Request.Messages[index].Content, rules)
		for callIndex := range req.Request.Messages[index].ToolCalls {
			req.Request.Messages[index].ToolCalls[callIndex].Function.Arguments = anonymize(req, req.Request.Messages[index].ToolCalls[callIndex].Function.Arguments, rules)
		}
	}
	if req.ResponseRequest != nil {
		req.ResponseRequest.Input = anonymizeAny(req, req.ResponseRequest.Input, rules)
		req.ResponseRequest.Instructions = anonymize(req, req.ResponseRequest.Instructions, rules)
	}
	if req.RerankRequest != nil {
		req.RerankRequest.Query = anonymize(req, req.RerankRequest.Query, rules)
		for index := range req.RerankRequest.Documents {
			req.RerankRequest.Documents[index] = anonymizeAny(req, req.RerankRequest.Documents[index], rules)
		}
	}
	return nil
}

func anonymize(req *RequestContext, value string, rules []AnonymizerRule) string {
	for _, rule := range rules {
		value = rule.apply(req, value)
	}
	return value
}

func (r AnonymizerRule) apply(req *RequestContext, value string) string {
	if r.CaptureGroup > 0 {
		return r.applyCaptureGroup(req, value)
	}
	return r.Pattern.ReplaceAllStringFunc(value, func(match string) string {
		if r.ShouldMask != nil && !r.ShouldMask(match) {
			return match
		}
		return req.rememberAnonymizedValue(r.Placeholder, match)
	})
}

func (r AnonymizerRule) applyCaptureGroup(req *RequestContext, value string) string {
	matches := r.Pattern.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return value
	}

	var result strings.Builder
	last := 0
	groupOffset := r.CaptureGroup * 2
	for _, match := range matches {
		start, end := match[groupOffset], match[groupOffset+1]
		if start < last || end < start {
			continue
		}
		captured := value[start:end]
		if r.ShouldMask != nil && !r.ShouldMask(captured) {
			continue
		}
		result.WriteString(value[last:start])
		result.WriteString(req.rememberAnonymizedValue(r.Placeholder, captured))
		last = end
	}
	if last == 0 {
		return value
	}
	result.WriteString(value[last:])
	return result.String()
}

func (req *RequestContext) rememberAnonymizedValue(basePlaceholder string, original string) string {
	if req.AnonymizationValues == nil {
		req.AnonymizationValues = map[string]string{}
	}

	for placeholder, value := range req.AnonymizationValues {
		if value == original && strings.HasPrefix(placeholder, strings.TrimSuffix(basePlaceholder, "}}")+"_") {
			return placeholder
		}
	}

	name := strings.TrimPrefix(strings.TrimSuffix(basePlaceholder, "}}"), "{{")
	placeholder := "{{" + name + "_" + nextPlaceholderIndex(req.AnonymizationValues, name) + "}}"
	req.AnonymizationValues[placeholder] = original
	return placeholder
}

func nextPlaceholderIndex(values map[string]string, name string) string {
	prefix := "{{" + name + "_"
	index := 1
	for {
		candidate := prefix + itoa(index) + "}}"
		if _, exists := values[candidate]; !exists {
			return itoa(index)
		}
		index++
	}
}

func DeanonymizeResponse(req *RequestContext, response *openai.ChatCompletionResponse) {
	if response == nil || len(req.AnonymizationValues) == 0 {
		return
	}

	for index := range response.Choices {
		response.Choices[index].Message.Content = DeanonymizeAny(response.Choices[index].Message.Content, req.AnonymizationValues)
		for callIndex := range response.Choices[index].Message.ToolCalls {
			response.Choices[index].Message.ToolCalls[callIndex].Function.Arguments = DeanonymizeText(response.Choices[index].Message.ToolCalls[callIndex].Function.Arguments, req.AnonymizationValues)
		}
	}
}

func DeanonymizeResponsesResponse(req *RequestContext, response *openai.ResponseResponse) {
	if response == nil || len(req.AnonymizationValues) == 0 {
		return
	}

	response.OutputText = DeanonymizeText(response.OutputText, req.AnonymizationValues)
	for outputIndex := range response.Output {
		for contentIndex := range response.Output[outputIndex].Content {
			response.Output[outputIndex].Content[contentIndex].Text = DeanonymizeText(response.Output[outputIndex].Content[contentIndex].Text, req.AnonymizationValues)
		}
		for summaryIndex := range response.Output[outputIndex].Summary {
			response.Output[outputIndex].Summary[summaryIndex].Text = DeanonymizeText(response.Output[outputIndex].Summary[summaryIndex].Text, req.AnonymizationValues)
		}
	}
}

func DeanonymizeText(value string, replacements map[string]string) string {
	for placeholder, original := range replacements {
		value = strings.ReplaceAll(value, placeholder, original)
	}
	return value
}

func DeanonymizeAny(value any, replacements map[string]string) any {
	return openai.TransformTextContent(value, func(text string) string {
		return DeanonymizeText(text, replacements)
	})
}

func anonymizeAny(req *RequestContext, value any, rules []AnonymizerRule) any {
	return openai.TransformTextContent(value, func(text string) string {
		return anonymize(req, text, rules)
	})
}

func buildAnonymizerRules(enabled []string) []AnonymizerRule {
	module, err := NewAnonymizerModuleFromConfig(true, defaultRuleConfig(), enabled...)
	if err != nil {
		panic(err)
	}
	return module.rules
}

func defaultRuleConfig() []RuleConfig {
	return []RuleConfig{
		{
			Name:        RuleAPIKey,
			Placeholder: "{{API_KEY}}",
			Pattern:     `(?i)\b(?:api[_-]?key|access[_-]?token|secret[_-]?key|client[_-]?secret|bearer)\s*[:=]\s*["']?[A-Za-z0-9._~+/=\-]{16,}["']?`,
		},
		{
			Name:        RuleJWT,
			Placeholder: "{{JWT}}",
			Pattern:     `\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`,
		},
		{
			Name:        RuleEmail,
			Placeholder: "{{EMAIL}}",
			Pattern:     `[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`,
		},
		{
			Name:        RuleBankCard,
			Placeholder: "{{BANK_CARD}}",
			Pattern:     `\b(?:\d[ -]?){13,19}\b`,
			Validator:   RuleValidatorLuhn,
		},
		{
			Name:        RulePassportRU,
			Placeholder: "{{PASSPORT_RU}}",
			Pattern:     `\b\d{4}\s+\d{6}\b`,
		},
		{
			Name:        RuleINN,
			Placeholder: "{{INN}}",
			Pattern:     `\b(?:\d{10}|\d{12})\b`,
		},
		{
			Name:        RulePhone,
			Placeholder: "{{PHONE}}",
			Pattern:     `(?i)(?:\+7|8|\+1|\+\d{1,3})?[\s(.-]*\d{3}[\s). -]*\d{3}[\s.-]*\d{2}[\s.-]*\d{2}\b`,
		},
		{
			Name:        RuleIP,
			Placeholder: "{{IP}}",
			Pattern:     `\b(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)\b`,
		},
		{
			Name:        RuleAddressRU,
			Placeholder: "{{ADDRESS_RU}}",
			Pattern:     `(?i)(?:г\.|город|ул\.|улица|пр-т|проспект|пер\.|переулок|д\.|дом|кв\.|квартира)\s*[^,;\n]{2,120}`,
		},
		{
			Name:        RulePersonRU,
			Placeholder: "{{PERSON_RU}}",
			Pattern:     `[А-ЯЁ][а-яё]+(?:\s+[А-ЯЁ][а-яё]+){1,2}`,
		},
		{
			Name:         RuleSecret,
			Placeholder:  "{{SECRET}}",
			Pattern:      `(?i)(?:^|[^\p{L}\p{N}_])(?:password|passwd|pwd|пароль)\s*[:=]\s*["']?([^"'\s,;]{6,})["']?`,
			CaptureGroup: 1,
		},
	}
}

func parseAnonymizerRules(value string) []string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" || value == "default" {
		return nil
	}
	if value == "none" || value == "off" || value == "disabled" {
		return []string{"__disabled__"}
	}

	parts := strings.Split(value, ",")
	rules := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name != "" {
			rules = append(rules, name)
		}
	}
	return rules
}

func isLikelyBankCard(value string) bool {
	digits := onlyDigits(value)
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}

	sum := 0
	alternate := false
	for i := len(digits) - 1; i >= 0; i-- {
		n := int(digits[i] - '0')
		if alternate {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		alternate = !alternate
	}
	return sum%10 == 0
}

func onlyDigits(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}

	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}
