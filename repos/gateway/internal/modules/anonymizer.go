package modules

import (
	"context"
	"os"
	"regexp"
	"strings"

	"ai-gateway-gateway/internal/openai"
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
	Name        string
	Placeholder string
	Pattern     *regexp.Regexp
	ShouldMask  func(value string) bool
}

func NewAnonymizerModule(required bool, enabledRules ...string) AnonymizerModule {
	return AnonymizerModule{
		required: required,
		rules:    buildAnonymizerRules(enabledRules),
	}
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

func (m AnonymizerModule) Handle(_ context.Context, req *RequestContext) error {
	for index := range req.Request.Messages {
		req.Request.Messages[index].Content = m.anonymizeAny(req, req.Request.Messages[index].Content)
		if req.Request.Messages[index].Refusal != nil {
			value := m.anonymize(req, *req.Request.Messages[index].Refusal)
			req.Request.Messages[index].Refusal = &value
		}
		for callIndex := range req.Request.Messages[index].ToolCalls {
			req.Request.Messages[index].ToolCalls[callIndex].Function.Arguments = m.anonymize(req, req.Request.Messages[index].ToolCalls[callIndex].Function.Arguments)
		}
	}
	if req.ResponseRequest != nil {
		req.ResponseRequest.Input = m.anonymizeAny(req, req.ResponseRequest.Input)
		req.ResponseRequest.Instructions = m.anonymize(req, req.ResponseRequest.Instructions)
	}
	if req.EmbeddingRequest != nil {
		req.EmbeddingRequest.Input = m.anonymizeAny(req, req.EmbeddingRequest.Input)
	}
	if req.RerankRequest != nil {
		req.RerankRequest.Query = m.anonymize(req, req.RerankRequest.Query)
		for index := range req.RerankRequest.Documents {
			req.RerankRequest.Documents[index] = m.anonymizeAny(req, req.RerankRequest.Documents[index])
		}
	}
	if req.ModerationRequest != nil {
		req.ModerationRequest.Input = m.anonymizeAny(req, req.ModerationRequest.Input)
	}
	if req.ImageGenerationRequest != nil {
		req.ImageGenerationRequest.Prompt = m.anonymize(req, req.ImageGenerationRequest.Prompt)
	}
	if req.ImageEditRequest != nil {
		req.ImageEditRequest.Prompt = m.anonymize(req, req.ImageEditRequest.Prompt)
	}
	if req.AudioTranscriptionRequest != nil {
		req.AudioTranscriptionRequest.Prompt = m.anonymize(req, req.AudioTranscriptionRequest.Prompt)
		for index := range req.AudioTranscriptionRequest.Keywords {
			req.AudioTranscriptionRequest.Keywords[index] = m.anonymize(req, req.AudioTranscriptionRequest.Keywords[index])
		}
		for index := range req.AudioTranscriptionRequest.KnownSpeakerNames {
			req.AudioTranscriptionRequest.KnownSpeakerNames[index] = m.anonymize(req, req.AudioTranscriptionRequest.KnownSpeakerNames[index])
		}
	}
	if req.OCRRequest != nil {
		req.OCRRequest.DocumentAnnotationPrompt = m.anonymize(req, req.OCRRequest.DocumentAnnotationPrompt)
	}
	return nil
}

func (m AnonymizerModule) anonymize(req *RequestContext, value string) string {
	for _, rule := range m.rules {
		value = rule.apply(req, value)
	}
	return value
}

func (r AnonymizerRule) apply(req *RequestContext, value string) string {
	return r.Pattern.ReplaceAllStringFunc(value, func(match string) string {
		if r.ShouldMask != nil && !r.ShouldMask(match) {
			return match
		}
		return req.rememberAnonymizedValue(r.Placeholder, match)
	})
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
		if response.Choices[index].Message.Refusal != nil {
			value := DeanonymizeText(*response.Choices[index].Message.Refusal, req.AnonymizationValues)
			response.Choices[index].Message.Refusal = &value
		}
		if audio := response.Choices[index].Message.Audio; audio != nil && audio.Transcript != nil {
			value := DeanonymizeText(*audio.Transcript, req.AnonymizationValues)
			audio.Transcript = &value
		}
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
		response.Output[outputIndex].Arguments = DeanonymizeText(response.Output[outputIndex].Arguments, req.AnonymizationValues)
		for contentIndex := range response.Output[outputIndex].Content {
			response.Output[outputIndex].Content[contentIndex].Text = DeanonymizeText(response.Output[outputIndex].Content[contentIndex].Text, req.AnonymizationValues)
			response.Output[outputIndex].Content[contentIndex].Refusal = DeanonymizeText(response.Output[outputIndex].Content[contentIndex].Refusal, req.AnonymizationValues)
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

func (m AnonymizerModule) anonymizeAny(req *RequestContext, value any) any {
	return openai.TransformTextContent(value, func(text string) string {
		return m.anonymize(req, text)
	})
}

func buildAnonymizerRules(enabled []string) []AnonymizerRule {
	selected := map[string]bool{}
	for _, name := range enabled {
		selected[name] = true
	}

	if len(selected) == 0 {
		for _, name := range defaultAnonymizerRules {
			selected[name] = true
		}
	}

	catalog := []AnonymizerRule{
		{
			Name:        RuleAPIKey,
			Placeholder: "{{API_KEY}}",
			Pattern:     regexp.MustCompile(`(?i)\b(?:api[_-]?key|access[_-]?token|secret[_-]?key|client[_-]?secret|bearer)\s*[:=]\s*["']?[A-Za-z0-9._~+/=\-]{16,}["']?`),
		},
		{
			Name:        RuleJWT,
			Placeholder: "{{JWT}}",
			Pattern:     regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`),
		},
		{
			Name:        RuleEmail,
			Placeholder: "{{EMAIL}}",
			Pattern:     regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`),
		},
		{
			Name:        RuleBankCard,
			Placeholder: "{{BANK_CARD}}",
			Pattern:     regexp.MustCompile(`\b(?:\d[ -]?){13,19}\b`),
			ShouldMask:  isLikelyBankCard,
		},
		{
			Name:        RulePassportRU,
			Placeholder: "{{PASSPORT_RU}}",
			Pattern:     regexp.MustCompile(`\b\d{4}\s+\d{6}\b`),
		},
		{
			Name:        RuleINN,
			Placeholder: "{{INN}}",
			Pattern:     regexp.MustCompile(`\b(?:\d{10}|\d{12})\b`),
		},
		{
			Name:        RulePhone,
			Placeholder: "{{PHONE}}",
			Pattern:     regexp.MustCompile(`(?i)(?:\+7|8|\+1|\+\d{1,3})?[\s(.-]*\d{3}[\s). -]*\d{3}[\s.-]*\d{2}[\s.-]*\d{2}\b`),
		},
		{
			Name:        RuleIP,
			Placeholder: "{{IP}}",
			Pattern:     regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)\b`),
		},
		{
			Name:        RuleAddressRU,
			Placeholder: "{{ADDRESS_RU}}",
			Pattern:     regexp.MustCompile(`(?i)(?:г\.|город|ул\.|улица|пр-т|проспект|пер\.|переулок|д\.|дом|кв\.|квартира)\s*[^,;\n]{2,120}`),
		},
		{
			Name:        RulePersonRU,
			Placeholder: "{{PERSON_RU}}",
			Pattern:     regexp.MustCompile(`[А-ЯЁ][а-яё]+(?:\s+[А-ЯЁ][а-яё]+){1,2}`),
		},
		{
			Name:        RuleSecret,
			Placeholder: "{{SECRET}}",
			Pattern:     regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|пароль)\s*[:=]\s*["']?[^"'\s,;]{6,}["']?`),
		},
	}

	rules := make([]AnonymizerRule, 0, len(catalog))
	for _, rule := range catalog {
		if selected["all"] || selected[rule.Name] {
			rules = append(rules, rule)
		}
	}
	return rules
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
