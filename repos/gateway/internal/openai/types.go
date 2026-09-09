package openai

import (
	"bytes"
	"encoding/json"
	"strings"
)

type ChatCompletionRequest struct {
	// RequireMatchedStop is an internal protocol requirement, never client JSON.
	RequireMatchedStop bool `json:"-"`
	ChatGenerationOptions
	Provider            string          `json:"provider,omitempty"`
	Model               string          `json:"model"`
	Messages            []Message       `json:"messages"`
	Tools               []Tool          `json:"tools,omitempty"`
	ToolChoice          any             `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool           `json:"parallel_tool_calls,omitempty"`
	ResponseFormat      *ResponseFormat `json:"response_format,omitempty"`
	Stream              bool            `json:"stream,omitempty"`
	MaxTokens           *int            `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int            `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	Stop                any             `json:"stop,omitempty"`
	Seed                *int64          `json:"seed,omitempty"`
}

type Message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type Tool struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

type FunctionDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
	Strict      *bool  `json:"strict,omitempty"`
}

type ToolCallExtraContent struct {
	Google *GoogleToolCallContent `json:"google,omitempty"`
}
type GoogleToolCallContent struct {
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

type ToolCall struct {
	ExtraContent *ToolCallExtraContent `json:"extra_content,omitempty"`
	Index        *int                  `json:"index,omitempty"`
	ID           string                `json:"id,omitempty"`
	Type         string                `json:"type"`
	Function     FunctionCall          `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (f *FunctionCall) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	f.Name = raw.Name
	if len(raw.Arguments) == 0 || bytes.Equal(raw.Arguments, []byte("null")) {
		f.Arguments = ""
		return nil
	}
	if raw.Arguments[0] == '"' {
		return json.Unmarshal(raw.Arguments, &f.Arguments)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw.Arguments); err != nil {
		return err
	}
	f.Arguments = compact.String()
	return nil
}

type ResponseFormat struct {
	Type       string            `json:"type"`
	JSONSchema *JSONSchemaFormat `json:"json_schema,omitempty"`
}

type JSONSchemaFormat struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Schema      any    `json:"schema"`
	Strict      *bool  `json:"strict,omitempty"`
}

type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type CompletionRequest struct {
	Provider         string         `json:"provider,omitempty"`
	Model            string         `json:"model"`
	Prompt           any            `json:"prompt,omitempty"`
	BestOf           *int           `json:"best_of,omitempty"`
	Echo             *bool          `json:"echo,omitempty"`
	FrequencyPenalty *float64       `json:"frequency_penalty,omitempty"`
	LogitBias        map[string]int `json:"logit_bias,omitempty"`
	Logprobs         *int           `json:"logprobs,omitempty"`
	MaxTokens        *int           `json:"max_tokens,omitempty"`
	N                *int           `json:"n,omitempty"`
	PresencePenalty  *float64       `json:"presence_penalty,omitempty"`
	Seed             *int64         `json:"seed,omitempty"`
	Stop             any            `json:"stop,omitempty"`
	Stream           bool           `json:"stream,omitempty"`
	Suffix           string         `json:"suffix,omitempty"`
	Temperature      *float64       `json:"temperature,omitempty"`
	TopP             *float64       `json:"top_p,omitempty"`
	User             string         `json:"user,omitempty"`
}

type CompletionResponse struct {
	ID                string             `json:"id"`
	Object            string             `json:"object"`
	Created           int64              `json:"created"`
	Model             string             `json:"model"`
	Choices           []CompletionChoice `json:"choices"`
	SystemFingerprint string             `json:"system_fingerprint,omitempty"`
	Usage             Usage              `json:"usage"`
}

type CompletionChoice struct {
	FinishReason string              `json:"finish_reason"`
	Index        int                 `json:"index"`
	Logprobs     *CompletionLogprobs `json:"logprobs,omitempty"`
	Text         string              `json:"text"`
}

type CompletionLogprobs struct {
	TextOffset    []int                `json:"text_offset"`
	TokenLogprobs []*float64           `json:"token_logprobs"`
	Tokens        []string             `json:"tokens"`
	TopLogprobs   []map[string]float64 `json:"top_logprobs"`
}

type Choice struct {
	StopSequence *string         `json:"stop_sequence,omitempty"`
	Logprobs     *ChoiceLogprobs `json:"logprobs,omitempty"`
	Index        int             `json:"index"`
	Message      Message         `json:"message"`
	FinishReason string          `json:"finish_reason"`
}

type Usage struct {
	PromptTokens            int                     `json:"prompt_tokens"`
	CompletionTokens        int                     `json:"completion_tokens"`
	TotalTokens             int                     `json:"total_tokens"`
	PromptTokensDetails     *PromptTokenDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *CompletionTokenDetails `json:"completion_tokens_details,omitempty"`
}

type CompletionTokenDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

type PromptTokenDetails struct {
	CachedTokens        int `json:"cached_tokens,omitempty"`
	CacheWriteTokens    int `json:"cache_write_tokens,omitempty"`
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"`
}

type EmbeddingRequest struct {
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model"`
	Input          any    `json:"input"`
	EncodingFormat string `json:"encoding_format,omitempty"`
	Dimensions     *int   `json:"dimensions,omitempty"`
	User           string `json:"user,omitempty"`
}

type EmbeddingResponse struct {
	// UsageReported distinguishes a provider-reported zero from absent usage.
	UsageReported bool        `json:"-"`
	Object        string      `json:"object"`
	Data          []Embedding `json:"data"`
	Model         string      `json:"model"`
	Usage         Usage       `json:"usage"`
}

type Embedding struct {
	Object          string    `json:"object"`
	Embedding       []float64 `json:"-"`
	EmbeddingBase64 string    `json:"-"`
	Index           int       `json:"index"`
}

type RerankRequest struct {
	Provider        string   `json:"provider,omitempty"`
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []any    `json:"documents"`
	TopN            *int     `json:"top_n,omitempty"`
	RankFields      []string `json:"rank_fields,omitempty"`
	ReturnDocuments *bool    `json:"return_documents,omitempty"`
	MaxChunksPerDoc *int     `json:"max_chunks_per_doc,omitempty"`
	MaxTokensPerDoc *int     `json:"max_tokens_per_doc,omitempty"`
}

type RerankResponse struct {
	ID      string              `json:"id,omitempty"`
	Results []RerankResult      `json:"results"`
	Meta    *RerankResponseMeta `json:"meta,omitempty"`
}

type RerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
	Document       any     `json:"document,omitempty"`
}

type RerankResponseMeta struct {
	APIVersion  map[string]any     `json:"api_version,omitempty"`
	BilledUnits *RerankBilledUnits `json:"billed_units,omitempty"`
	Tokens      *RerankTokens      `json:"tokens,omitempty"`
}

type RerankBilledUnits struct {
	SearchUnits int `json:"search_units,omitempty"`
	TotalTokens int `json:"total_tokens,omitempty"`
}

type RerankTokens struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}

func RerankDocumentText(request RerankRequest) (string, bool) {
	if strings.TrimSpace(request.Query) == "" || len(request.Documents) == 0 {
		return "", false
	}
	parts := []string{request.Query}
	for _, document := range request.Documents {
		texts, ok := RerankDocumentStrings(document, request.RankFields)
		if !ok {
			return "", false
		}
		parts = append(parts, texts...)
	}
	return strings.Join(parts, "\n"), true
}

func RerankDocumentStrings(document any, rankFields []string) ([]string, bool) {
	switch value := document.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return nil, false
		}
		return []string{value}, true
	case map[string]any:
		fields := rankFields
		if len(fields) == 0 {
			fields = []string{"text"}
		}
		texts := make([]string, 0, len(fields))
		for _, field := range fields {
			text, ok := value[field].(string)
			if ok && strings.TrimSpace(text) != "" {
				texts = append(texts, text)
			}
		}
		return texts, len(texts) > 0
	default:
		return nil, false
	}
}

func EmbeddingInputStrings(value any) ([]string, bool) {
	info, err := InspectEmbeddingInput(value)
	if err != nil || info.Tokenized() {
		return nil, false
	}
	return info.Texts, true
}

func EmbeddingInputText(value any) string {
	items, ok := EmbeddingInputStrings(value)
	if !ok {
		return ""
	}
	text := ""
	for _, item := range items {
		if text != "" {
			text += "\n"
		}
		text += item
	}
	return text
}

type ResponseRequest struct {
	Metadata          map[string]string  `json:"metadata,omitempty"`
	TopLogprobs       *int               `json:"top_logprobs,omitempty"`
	Truncation        *string            `json:"truncation,omitempty"`
	Reasoning         *ResponseReasoning `json:"reasoning,omitempty"`
	Store             *bool              `json:"store,omitempty"`
	Include           []string           `json:"include,omitempty"`
	Provider          string             `json:"provider,omitempty"`
	Model             string             `json:"model"`
	Input             any                `json:"input"`
	Instructions      string             `json:"instructions,omitempty"`
	Tools             []ResponseTool     `json:"tools,omitempty"`
	ToolChoice        any                `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool              `json:"parallel_tool_calls,omitempty"`
	Text              any                `json:"text,omitempty"`
	PreviousResponse  string             `json:"previous_response_id,omitempty"`
	SafetyIdentifier  string             `json:"safety_identifier,omitempty"`
	PromptCacheKey    string             `json:"prompt_cache_key,omitempty"`
	ServiceTier       string             `json:"service_tier,omitempty"`
	Stream            bool               `json:"stream,omitempty"`
	MaxOutputTokens   *int               `json:"max_output_tokens,omitempty"`
	MaxTokens         *int               `json:"max_tokens,omitempty"`
	Temperature       *float64           `json:"temperature,omitempty"`
	TopP              *float64           `json:"top_p,omitempty"`
}

type ResponseInputTokenCountRequest struct {
	Provider          string             `json:"provider,omitempty"`
	Model             string             `json:"model"`
	Input             any                `json:"input"`
	Instructions      string             `json:"instructions,omitempty"`
	Tools             []ResponseTool     `json:"tools,omitempty"`
	ToolChoice        any                `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool              `json:"parallel_tool_calls,omitempty"`
	Text              any                `json:"text,omitempty"`
	PreviousResponse  string             `json:"previous_response_id,omitempty"`
	Reasoning         *ResponseReasoning `json:"reasoning,omitempty"`
	Truncation        *string            `json:"truncation,omitempty"`
}

func (r ResponseInputTokenCountRequest) ResponseRequest() ResponseRequest {
	return ResponseRequest{
		Provider: r.Provider, Model: r.Model, Input: r.Input, Instructions: r.Instructions,
		Tools: r.Tools, ToolChoice: r.ToolChoice, ParallelToolCalls: r.ParallelToolCalls,
		Text: r.Text, PreviousResponse: r.PreviousResponse, Reasoning: r.Reasoning,
		Truncation: r.Truncation,
	}
}

type ResponseInputTokenCount struct {
	Object      string `json:"object"`
	InputTokens int    `json:"input_tokens"`
}

type ResponseTool struct {
	Type              string            `json:"type"`
	Name              string            `json:"name,omitempty"`
	Description       string            `json:"description,omitempty"`
	Parameters        any               `json:"parameters,omitempty"`
	Strict            *bool             `json:"strict,omitempty"`
	ServerLabel       string            `json:"server_label,omitempty"`
	ServerURL         string            `json:"server_url,omitempty"`
	ServerDescription string            `json:"server_description,omitempty"`
	AllowedTools      []string          `json:"allowed_tools,omitempty"`
	RequireApproval   any               `json:"require_approval,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
}

type ResponseResponse struct {
	Metadata map[string]string `json:"metadata,omitempty"`
	// InputTokensReported distinguishes an explicit upstream zero from absent usage.
	InputTokensReported bool                       `json:"-"`
	Error               *ResponseError             `json:"error,omitempty"`
	IncompleteDetails   *ResponseIncompleteDetails `json:"incomplete_details,omitempty"`
	ID                  string                     `json:"id"`
	Object              string                     `json:"object"`
	CreatedAt           int64                      `json:"created_at,omitempty"`
	Status              string                     `json:"status,omitempty"`
	Model               string                     `json:"model"`
	Output              []ResponseOutputItem       `json:"output,omitempty"`
	OutputText          string                     `json:"output_text,omitempty"`
	Usage               ResponseUsage              `json:"usage,omitempty"`
}

type ResponseInputItemList struct {
	Object  string            `json:"object"`
	Data    []json.RawMessage `json:"data"`
	FirstID string            `json:"first_id,omitempty"`
	LastID  string            `json:"last_id,omitempty"`
	HasMore bool              `json:"has_more"`
}

type ResponseDeletion struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}

type ResponseCompactRequest struct {
	Provider     string `json:"provider,omitempty"`
	Model        string `json:"model"`
	Input        any    `json:"input"`
	Instructions string `json:"instructions,omitempty"`
}

type CompactedResponse struct {
	ID        string            `json:"id"`
	Object    string            `json:"object"`
	CreatedAt int64             `json:"created_at,omitempty"`
	Output    []json.RawMessage `json:"output"`
	Usage     ResponseUsage     `json:"usage"`
}

type ResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ResponseIncompleteDetails struct {
	Reason string `json:"reason"`
}

type ResponseOutputItem struct {
	Phase            *string                 `json:"phase,omitempty"`
	EncryptedContent *string                 `json:"encrypted_content,omitempty"`
	ID               string                  `json:"id,omitempty"`
	Type             string                  `json:"type"`
	Status           string                  `json:"status,omitempty"`
	Role             string                  `json:"role,omitempty"`
	Name             string                  `json:"name,omitempty"`
	CallID           string                  `json:"call_id,omitempty"`
	Arguments        string                  `json:"arguments,omitempty"`
	Content          []ResponseOutputContent `json:"content,omitempty"`
	Summary          []ResponseOutputContent `json:"summary,omitempty"`
}

type ResponseOutputContent struct {
	Logprobs    []TokenLogprob    `json:"logprobs,omitempty"`
	Annotations []json.RawMessage `json:"annotations,omitempty"`
	Refusal     string            `json:"refusal,omitempty"`
	Type        string            `json:"type"`
	Text        string            `json:"text,omitempty"`
}

type ResponseUsage struct {
	OutputTokensDetails *CompletionTokenDetails `json:"output_tokens_details,omitempty"`
	InputTokens         int                     `json:"input_tokens,omitempty"`
	OutputTokens        int                     `json:"output_tokens,omitempty"`
	TotalTokens         int                     `json:"total_tokens,omitempty"`
	InputTokensDetails  *InputTokenDetails      `json:"input_tokens_details,omitempty"`
}

type InputTokenDetails struct {
	CachedTokens        int `json:"cached_tokens,omitempty"`
	CacheWriteTokens    int `json:"cache_write_tokens,omitempty"`
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"`
}

type ModelsResponse struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func ContentText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		text := ""
		for _, item := range typed {
			part := ContentText(item)
			if part == "" {
				continue
			}
			if text != "" {
				text += " "
			}
			text += part
		}
		return text
	case map[string]any:
		if text, ok := typed["text"].(string); ok {
			return text
		}
		for _, key := range []string{"arguments", "output", "content"} {
			if part := ContentText(typed[key]); part != "" {
				return part
			}
		}
		if _, ok := typed["type"]; ok {
			return ""
		}
		text := ""
		for _, item := range typed {
			part := ContentText(item)
			if part == "" {
				continue
			}
			if text != "" {
				text += " "
			}
			text += part
		}
		return text
	default:
		return ""
	}
}

type ResponseReasoning struct {
	Effort          *string `json:"effort,omitempty"`
	Summary         *string `json:"summary,omitempty"`
	GenerateSummary *string `json:"generate_summary,omitempty"`
	Context         *string `json:"context,omitempty"`
	Mode            *string `json:"mode,omitempty"`
}
