package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"
)

type ChatCompletionRequest struct {
	// RequireMatchedStop is an internal protocol requirement, never client JSON.
	RequireMatchedStop bool `json:"-"`
	// AllowZeroMaxTokens permits the native Messages cache-population contract.
	AllowZeroMaxTokens bool `json:"-"`
	// BedrockInvoke selects the validated Anthropic Messages dialect of InvokeModel.
	BedrockInvoke bool `json:"-"`
	// NativeInputTokens reserves provider-native context omitted from the public Chat wire shape.
	NativeInputTokens int `json:"-"`
	// GeminiSafetySettings contains validated native safety controls.
	GeminiSafetySettings []GeminiSafetySetting `json:"-"`
	// GeminiCachedContent is an owner-checked native cached content resource.
	// Endpoint and deployment pinning prevent routing it under another provider credential.
	GeminiCachedContent           string `json:"-"`
	GeminiCachedContentEndpoint   string `json:"-"`
	GeminiCachedContentDeployment string `json:"-"`
	GeminiCachedContentPolicy     string `json:"-"`
	// GeminiCodeExecution enables the native server-side code execution tool.
	GeminiCodeExecution bool `json:"-"`
	// GeminiURLContext enables the native server-side URL retrieval tool.
	GeminiURLContext bool `json:"-"`
	// GeminiGoogleMaps enables native location grounding. The optional location
	// is validated at the native request boundary.
	GeminiGoogleMaps        bool          `json:"-"`
	GeminiRetrievalLocation *GeminiLatLng `json:"-"`
	// GeminiAudioTimestamp enables Vertex audio timestamp understanding for
	// requests that contain validated audio input.
	GeminiAudioTimestamp *bool `json:"-"`
	// GeminiMediaResolution controls the native input-media token resolution.
	GeminiMediaResolution string `json:"-"`
	// GeminiFileSearch contains validated provider-managed retrieval stores.
	GeminiFileSearch *GeminiFileSearchConfig `json:"-"`
	// GeminiComputerUse contains validated client-executed computer controls.
	GeminiComputerUse *GeminiComputerUseConfig `json:"-"`
	// GeminiMCPServerIDs are authenticated gateway registry references.
	GeminiMCPServerIDs []string `json:"-"`
	// GeminiMCPServers are resolved only after connector authorization.
	GeminiMCPServers []GeminiMCPServer `json:"-"`
	// GeminiMCPConnectorIDs are canonical authorized registry identities.
	GeminiMCPConnectorIDs []string `json:"-"`
	// AnthropicSkills contains validated native Messages skill references.
	AnthropicSkills      []AnthropicSkillReference `json:"-"`
	AnthropicContainerID string                    `json:"-"`
	// AnthropicCodeExecution enables the native managed code execution tool.
	AnthropicCodeExecution     bool   `json:"-"`
	AnthropicCodeExecutionType string `json:"-"`
	// AnthropicToolSearch selects the validated native tool-search variant.
	AnthropicToolSearch string `json:"-"`
	// AnthropicClientTools contains validated provider-defined tools that the caller executes.
	AnthropicClientTools []AnthropicClientTool `json:"-"`
	// AnthropicClientToolsets contains validated provider-defined client toolsets.
	AnthropicClientToolsets []AnthropicClientToolset `json:"-"`
	// AnthropicThinking contains a validated native Messages thinking policy.
	AnthropicThinking *AnthropicThinkingConfig `json:"-"`
	// AnthropicCacheControl contains a validated native top-level prompt-cache marker.
	AnthropicCacheControl *PromptCacheBreakpoint `json:"-"`
	// AnthropicInferenceGeo pins native Messages inference to an allowed geography.
	AnthropicInferenceGeo string `json:"-"`
	// AnthropicContextManagement contains validated native server-side context edits.
	AnthropicContextManagement json.RawMessage `json:"-"`
	// Bedrock native controls cannot be supplied through the public Chat wire shape.
	BedrockServiceTier                       string                  `json:"-"`
	BedrockPerformanceLatency                string                  `json:"-"`
	BedrockAdditionalModelResponseFieldPaths []string                `json:"-"`
	BedrockAdditionalModelRequestFields      json.RawMessage         `json:"-"`
	BedrockRequestMetadata                   map[string]string       `json:"-"`
	BedrockGuardrailConfig                   *BedrockGuardrailConfig `json:"-"`
	ChatGenerationOptions
	Provider            string                `json:"provider,omitempty"`
	Model               string                `json:"model"`
	Messages            []Message             `json:"messages"`
	Functions           []FunctionDefinition  `json:"functions,omitempty"`
	FunctionCall        *LegacyFunctionChoice `json:"function_call,omitempty"`
	Tools               []Tool                `json:"tools,omitempty"`
	ToolChoice          any                   `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool                 `json:"parallel_tool_calls,omitempty"`
	ResponseFormat      *ResponseFormat       `json:"response_format,omitempty"`
	Stream              bool                  `json:"stream,omitempty"`
	StreamOptions       *ChatStreamOptions    `json:"stream_options,omitempty"`
	MaxTokens           *int                  `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                  `json:"max_completion_tokens,omitempty"`
	Temperature         *float64              `json:"temperature,omitempty"`
	TopP                *float64              `json:"top_p,omitempty"`
	Stop                any                   `json:"stop,omitempty"`
	Seed                *int64                `json:"seed,omitempty"`
}

type AnthropicThinkingConfig struct {
	Type         string
	BudgetTokens *int
	Display      string
}

type AnthropicSkillReference struct {
	Type    string `json:"type"`
	SkillID string `json:"skill_id"`
	Version string `json:"version,omitempty"`
}

type AnthropicClientTool struct {
	Type                  string
	Name                  string
	AllowedCallers        []string
	PromptCacheBreakpoint *PromptCacheBreakpoint
	DeferLoading          bool
	MaxCharacters         *int
}

type AnthropicClientToolset struct {
	Type                  string
	Name                  string
	Configs               map[string]AnthropicToolsetMemberConfig
	AllowedCallers        []string
	PromptCacheBreakpoint *PromptCacheBreakpoint
}

type AnthropicToolsetMemberConfig struct {
	Enabled      *bool
	DeferLoading *bool
}

type GeminiSafetySetting struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

type GeminiLatLng struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type ChatStreamOptions struct {
	IncludeUsage       bool  `json:"include_usage"`
	IncludeObfuscation *bool `json:"include_obfuscation,omitempty"`
}

type ResponseStreamOptions struct {
	IncludeObfuscation *bool `json:"include_obfuscation,omitempty"`
}

type Message struct {
	Role        string           `json:"role"`
	Content     any              `json:"content"`
	Prefix      *bool            `json:"prefix,omitempty"`
	Refusal     *string          `json:"refusal,omitempty"`
	Annotations []ChatAnnotation `json:"annotations,omitempty"`
	Audio       *ChatAudio       `json:"audio,omitempty"`
	Name        string           `json:"name,omitempty"`
	ToolCallID  string           `json:"tool_call_id,omitempty"`
	// ToolResultError preserves the native meaning of a failed client tool result.
	ToolResultError  bool             `json:"-"`
	ToolCalls        []ToolCall       `json:"tool_calls,omitempty"`
	FunctionCall     *FunctionCall    `json:"function_call,omitempty"`
	Reasoning        []ReasoningBlock `json:"reasoning,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	// NativeContent is an internal, validated response representation used by
	// protocol adapters that must preserve provider-native content block order.
	NativeContent []json.RawMessage `json:"-"`
	// AnthropicDocumentCitations aligns native citation controls with document parts.
	AnthropicDocumentCitations []bool `json:"-"`
	// AnthropicDocumentMetadata aligns policy-visible title and context with document parts.
	AnthropicDocumentMetadata []DocumentMetadata `json:"-"`
	// GeminiCodeExecutionParts preserves validated native execution output order.
	GeminiCodeExecutionParts []GeminiCodeExecutionPart `json:"-"`
}

type DocumentMetadata struct {
	Title   string `json:"title,omitempty"`
	Context string `json:"context,omitempty"`
}

type GeminiCodeExecutionPart struct {
	Index  int
	Code   *GeminiExecutableCode
	Result *GeminiCodeExecutionResult
}

type GeminiExecutableCode struct {
	ID       string `json:"id,omitempty"`
	Language string `json:"language"`
	Code     string `json:"code"`
}

type GeminiCodeExecutionResult struct {
	ID      string `json:"id,omitempty"`
	Outcome string `json:"outcome"`
	Output  string `json:"output,omitempty"`
}

const MaxChatReasoningContentBytes = 1 << 20

func ValidateChatReasoningContent(role, content string) error {
	if content == "" {
		return nil
	}
	if role != "assistant" {
		return errors.New("reasoning_content requires role=assistant")
	}
	if len(content) > MaxChatReasoningContentBytes {
		return errors.New("reasoning_content exceeds its size limit")
	}
	return nil
}

// ReasoningBlock preserves signed and redacted reasoning returned by native
// providers so it can be supplied on a later turn without exposing it as text.
type ReasoningBlock struct {
	Index     *int   `json:"index,omitempty"`
	Type      string `json:"type"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`
}

func ValidateReasoningBlocks(blocks []ReasoningBlock) error {
	return validateReasoningBlocks(blocks, true)
}

// ValidateBedrockReasoningBlocks accepts unsigned reasoning text because the
// native Bedrock contract makes the signature optional.
func ValidateBedrockReasoningBlocks(blocks []ReasoningBlock) error {
	return validateReasoningBlocks(blocks, false)
}

func validateReasoningBlocks(blocks []ReasoningBlock, requireSignature bool) error {
	if len(blocks) > 128 {
		return errors.New("message contains more than 128 reasoning blocks")
	}
	total := 0
	indexes := map[int]bool{}
	for _, block := range blocks {
		if block.Index != nil && (*block.Index < 0 || *block.Index >= 128) {
			return errors.New("reasoning block index is outside the supported range")
		}
		if block.Index != nil {
			if indexes[*block.Index] {
				return errors.New("reasoning block indexes must be unique")
			}
			indexes[*block.Index] = true
		}
		switch block.Type {
		case "thinking":
			if block.Thinking == "" || requireSignature && block.Signature == "" || block.Data != "" {
				return errors.New("thinking blocks require thinking and a valid signature policy")
			}
			total += len(block.Thinking) + len(block.Signature)
		case "redacted_thinking":
			if block.Data == "" || block.Thinking != "" || block.Signature != "" {
				return errors.New("redacted_thinking blocks require data")
			}
			total += len(block.Data)
		default:
			return errors.New("unsupported reasoning block type")
		}
		if total > 1<<20 {
			return errors.New("reasoning blocks exceed their size limit")
		}
	}
	return nil
}

type LegacyFunctionChoice struct {
	Mode string
	Name string
}

func (c *LegacyFunctionChoice) UnmarshalJSON(data []byte) error {
	var mode string
	if json.Unmarshal(data, &mode) == nil {
		if mode != "none" && mode != "auto" {
			return errors.New("function_call must be none, auto, or a named function")
		}
		c.Mode, c.Name = mode, ""
		return nil
	}
	var named struct {
		Name string `json:"name"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&named); err != nil || strings.TrimSpace(named.Name) == "" {
		return errors.New("function_call must be none, auto, or a named function")
	}
	c.Mode, c.Name = "", named.Name
	return nil
}

func (c LegacyFunctionChoice) MarshalJSON() ([]byte, error) {
	if c.Name != "" {
		return json.Marshal(struct {
			Name string `json:"name"`
		}{c.Name})
	}
	return json.Marshal(c.Mode)
}

type ChatAnnotation struct {
	Type           string              `json:"type"`
	URLCitation    *ChatURLCitation    `json:"url_citation,omitempty"`
	SourceCitation *ChatSourceCitation `json:"source_citation,omitempty"`
}

type ChatURLCitation struct {
	EndIndex   int    `json:"end_index"`
	StartIndex int    `json:"start_index"`
	Title      string `json:"title"`
	URL        string `json:"url"`
}

type ChatSourceCitation struct {
	EndIndex          int      `json:"end_index"`
	StartIndex        int      `json:"start_index"`
	Title             string   `json:"title,omitempty"`
	Source            string   `json:"source,omitempty"`
	SourceContent     []string `json:"source_content,omitempty"`
	LocationType      string   `json:"location_type"`
	DocumentIndex     *int     `json:"document_index,omitempty"`
	SearchResultIndex *int     `json:"search_result_index,omitempty"`
	LocationStart     int      `json:"location_start"`
	LocationEnd       int      `json:"location_end"`
}

func ValidateChatAnnotations(annotations []ChatAnnotation) error {
	if len(annotations) > 128 {
		return errors.New("chat completion contains more than 128 annotations")
	}
	for _, annotation := range annotations {
		switch annotation.Type {
		case "url_citation":
			citation := annotation.URLCitation
			if citation == nil || annotation.SourceCitation != nil {
				return errors.New("invalid chat URL citation")
			}
			parsed, err := url.Parse(citation.URL)
			if citation.StartIndex < 0 || citation.EndIndex < citation.StartIndex || citation.Title == "" || utf8.RuneCountInString(citation.Title) > 2048 || err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || utf8.RuneCountInString(citation.URL) > 8192 {
				return errors.New("invalid chat URL citation")
			}
		case "source_citation":
			citation := annotation.SourceCitation
			if citation == nil || annotation.URLCitation != nil || !validChatSourceCitation(*citation) {
				return errors.New("invalid chat source citation")
			}
		default:
			return errors.New("unsupported chat annotation type")
		}
	}
	return nil
}

func validChatSourceCitation(citation ChatSourceCitation) bool {
	if citation.StartIndex < 0 || citation.EndIndex < citation.StartIndex || citation.LocationStart < 0 || citation.LocationEnd < citation.LocationStart || utf8.RuneCountInString(citation.Title) > 2048 || utf8.RuneCountInString(citation.Source) > 8192 || len(citation.SourceContent) > 16 {
		return false
	}
	totalSourceRunes := 0
	for _, content := range citation.SourceContent {
		totalSourceRunes += utf8.RuneCountInString(content)
		if totalSourceRunes > 65536 {
			return false
		}
	}
	switch citation.LocationType {
	case "document_char", "document_chunk", "document_page":
		return citation.DocumentIndex != nil && *citation.DocumentIndex >= 0 && citation.SearchResultIndex == nil
	case "search_result":
		return citation.SearchResultIndex != nil && *citation.SearchResultIndex >= 0 && citation.DocumentIndex == nil
	default:
		return false
	}
}

type Tool struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

type FunctionDefinition struct {
	Name                  string                 `json:"name"`
	Description           string                 `json:"description,omitempty"`
	Parameters            any                    `json:"parameters,omitempty"`
	Strict                *bool                  `json:"strict,omitempty"`
	PromptCacheBreakpoint *PromptCacheBreakpoint `json:"prompt_cache_breakpoint,omitempty"`
	// DeferLoading keeps a native Messages function schema out of the initial context.
	DeferLoading bool `json:"-"`
}

type PromptCacheBreakpoint struct {
	Mode string `json:"mode"`
	TTL  string `json:"ttl,omitempty"`
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
	// ToolsetName preserves provider-native client toolset identity for Messages responses.
	ToolsetName string `json:"-"`
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
	// NativeContainer preserves a validated provider container descriptor for
	// protocol adapters that expose managed execution state.
	NativeContainer         json.RawMessage   `json:"-"`
	NativeContextManagement json.RawMessage   `json:"-"`
	ProviderEndpoint        string            `json:"-"`
	ID                      string            `json:"id"`
	Object                  string            `json:"object"`
	Created                 int64             `json:"created,omitempty"`
	Model                   string            `json:"model"`
	Metadata                map[string]string `json:"metadata,omitempty"`
	ServiceTier             string            `json:"service_tier,omitempty"`
	SystemFingerprint       string            `json:"system_fingerprint,omitempty"`
	Choices                 []Choice          `json:"choices"`
	Usage                   Usage             `json:"usage"`
}

type CompletionRequest struct {
	Provider         string            `json:"provider,omitempty"`
	Model            string            `json:"model"`
	Prompt           any               `json:"prompt,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	BestOf           *int              `json:"best_of,omitempty"`
	Echo             *bool             `json:"echo,omitempty"`
	FrequencyPenalty *float64          `json:"frequency_penalty,omitempty"`
	LogitBias        map[string]int    `json:"logit_bias,omitempty"`
	Logprobs         *int              `json:"logprobs,omitempty"`
	MaxTokens        *int              `json:"max_tokens,omitempty"`
	MinTokens        *int              `json:"min_tokens,omitempty"`
	N                *int              `json:"n,omitempty"`
	PresencePenalty  *float64          `json:"presence_penalty,omitempty"`
	PromptCacheKey   string            `json:"prompt_cache_key,omitempty"`
	Seed             *int64            `json:"seed,omitempty"`
	Stop             any               `json:"stop,omitempty"`
	Stream           bool              `json:"stream,omitempty"`
	Suffix           string            `json:"suffix,omitempty"`
	Temperature      *float64          `json:"temperature,omitempty"`
	TopP             *float64          `json:"top_p,omitempty"`
	User             string            `json:"user,omitempty"`
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
	StopSequence             *string         `json:"stop_sequence,omitempty"`
	Logprobs                 *ChoiceLogprobs `json:"logprobs,omitempty"`
	Index                    int             `json:"index"`
	Message                  Message         `json:"message"`
	FinishReason             string          `json:"finish_reason"`
	GeminiGroundingMetadata  json.RawMessage `json:"gemini_grounding_metadata,omitempty"`
	GeminiURLContextMetadata json.RawMessage `json:"gemini_url_context_metadata,omitempty"`
}

type Usage struct {
	// SearchRequests is internal provider usage used for billing. It is not part
	// of the OpenAI-compatible response payload.
	SearchRequests       int  `json:"-"`
	ToolRequests         int  `json:"-"`
	ToolRequestsReported bool `json:"-"`
	// ProviderToolInputTokens is included in PromptTokens and preserves a native usage split.
	ProviderToolInputTokens int                     `json:"-"`
	ProviderCostUSDTicks    *int64                  `json:"-"`
	InferenceGeo            string                  `json:"-"`
	PromptTokens            int                     `json:"prompt_tokens"`
	CompletionTokens        int                     `json:"completion_tokens"`
	TotalTokens             int                     `json:"total_tokens"`
	PromptTokensDetails     *PromptTokenDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *CompletionTokenDetails `json:"completion_tokens_details,omitempty"`
}

func (u *Usage) UnmarshalJSON(data []byte) error {
	var wire struct {
		PromptTokens            int                     `json:"prompt_tokens"`
		CompletionTokens        int                     `json:"completion_tokens"`
		TotalTokens             int                     `json:"total_tokens"`
		PromptTokensDetails     *PromptTokenDetails     `json:"prompt_tokens_details"`
		CompletionTokensDetails *CompletionTokenDetails `json:"completion_tokens_details"`
		PromptCacheHitTokens    *int                    `json:"prompt_cache_hit_tokens"`
		ProviderCostUSDTicks    *int64                  `json:"cost_in_usd_ticks"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	u.PromptTokens = wire.PromptTokens
	u.CompletionTokens = wire.CompletionTokens
	u.TotalTokens = wire.TotalTokens
	u.PromptTokensDetails = wire.PromptTokensDetails
	u.CompletionTokensDetails = wire.CompletionTokensDetails
	u.ProviderCostUSDTicks = wire.ProviderCostUSDTicks
	if wire.PromptCacheHitTokens != nil {
		if u.PromptTokensDetails == nil {
			u.PromptTokensDetails = &PromptTokenDetails{}
		}
		u.PromptTokensDetails.CachedTokens = *wire.PromptCacheHitTokens
	}
	return nil
}

type CompletionTokenDetails struct {
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens,omitempty"`
	AudioTokens              int `json:"audio_tokens,omitempty"`
	CachedTokens             int `json:"cached_tokens,omitempty"`
	ReasoningTokens          int `json:"reasoning_tokens,omitempty"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens,omitempty"`
	TextTokens               int `json:"text_tokens,omitempty"`
}

type PromptTokenDetails struct {
	CachedTokens        int `json:"cached_tokens,omitempty"`
	CacheWriteTokens    int `json:"cache_write_tokens,omitempty"`
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"`
	AudioTokens         int `json:"audio_tokens,omitempty"`
	ImageTokens         int `json:"image_tokens,omitempty"`
	TextTokens          int `json:"text_tokens,omitempty"`
}

type EmbeddingRequest struct {
	Provider       string            `json:"provider,omitempty"`
	Model          string            `json:"model"`
	Input          any               `json:"input"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	InputType      string            `json:"input_type,omitempty"`
	EncodingFormat string            `json:"encoding_format,omitempty"`
	Dimensions     *int              `json:"dimensions,omitempty"`
	OutputDType    string            `json:"output_dtype,omitempty"`
	User           string            `json:"user,omitempty"`
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
	Truncate        string   `json:"truncate,omitempty"`
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
	SearchUnits float64 `json:"search_units,omitempty"`
	TotalTokens int     `json:"total_tokens,omitempty"`
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
	// NativeInputTokens reserves provider-native context omitted from the public Responses wire shape.
	NativeInputTokens    int                    `json:"-"`
	Metadata             map[string]string      `json:"metadata,omitempty"`
	ContextManagement    []ResponseContextEntry `json:"context_management,omitempty"`
	Moderation           *ProviderModeration    `json:"moderation,omitempty"`
	TopLogprobs          *int                   `json:"top_logprobs,omitempty"`
	Truncation           *string                `json:"truncation,omitempty"`
	Reasoning            *ResponseReasoning     `json:"reasoning,omitempty"`
	Store                *bool                  `json:"store,omitempty"`
	Include              []string               `json:"include,omitempty"`
	Provider             string                 `json:"provider,omitempty"`
	Model                string                 `json:"model"`
	Input                any                    `json:"input"`
	Instructions         string                 `json:"instructions,omitempty"`
	Tools                []ResponseTool         `json:"tools,omitempty"`
	ToolChoice           any                    `json:"tool_choice,omitempty"`
	ParallelToolCalls    *bool                  `json:"parallel_tool_calls,omitempty"`
	Text                 any                    `json:"text,omitempty"`
	PreviousResponse     string                 `json:"previous_response_id,omitempty"`
	Conversation         *ResponseConversation  `json:"conversation,omitempty"`
	User                 string                 `json:"user,omitempty"`
	SafetyIdentifier     string                 `json:"safety_identifier,omitempty"`
	PromptCacheKey       string                 `json:"prompt_cache_key,omitempty"`
	PromptCacheOptions   *PromptCacheOptions    `json:"prompt_cache_options,omitempty"`
	PromptCacheRetention string                 `json:"prompt_cache_retention,omitempty"`
	ServiceTier          string                 `json:"service_tier,omitempty"`
	Background           bool                   `json:"background,omitempty"`
	Stream               bool                   `json:"stream,omitempty"`
	StreamOptions        *ResponseStreamOptions `json:"stream_options,omitempty"`
	MaxOutputTokens      *int                   `json:"max_output_tokens,omitempty"`
	MaxTokens            *int                   `json:"max_tokens,omitempty"`
	Temperature          *float64               `json:"temperature,omitempty"`
	TopP                 *float64               `json:"top_p,omitempty"`
	FrequencyPenalty     *float64               `json:"frequency_penalty,omitempty"`
	PresencePenalty      *float64               `json:"presence_penalty,omitempty"`
	MaxToolCalls         *int                   `json:"max_tool_calls,omitempty"`
}

type ResponseConversation struct {
	ID string `json:"id"`
}

func (c *ResponseConversation) UnmarshalJSON(data []byte) error {
	var id string
	if len(bytes.TrimSpace(data)) > 0 && bytes.TrimSpace(data)[0] == '"' && json.Unmarshal(data, &id) == nil {
		c.ID = id
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil || len(object) != 1 {
		if err == nil {
			err = errors.New("conversation must be a string or object containing only id")
		}
		return err
	}
	return json.Unmarshal(object["id"], &c.ID)
}

type ResponseContextEntry struct {
	Type             string `json:"type"`
	CompactThreshold *int   `json:"compact_threshold,omitempty"`
}

type ProviderModeration struct {
	Model  string                    `json:"model"`
	Policy *ProviderModerationPolicy `json:"policy,omitempty"`
}

type ProviderModerationPolicy struct {
	Input  *ProviderModerationRule `json:"input,omitempty"`
	Output *ProviderModerationRule `json:"output,omitempty"`
}

type ProviderModerationRule struct {
	Mode string `json:"mode"`
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
	Type              string                     `json:"type"`
	Name              string                     `json:"name,omitempty"`
	Description       string                     `json:"description,omitempty"`
	Parameters        any                        `json:"parameters,omitempty"`
	Strict            *bool                      `json:"strict,omitempty"`
	ServerLabel       string                     `json:"server_label,omitempty"`
	ServerURL         string                     `json:"server_url,omitempty"`
	ServerDescription string                     `json:"server_description,omitempty"`
	AllowedTools      []string                   `json:"allowed_tools,omitempty"`
	AllowedCallers    []string                   `json:"allowed_callers,omitempty"`
	RequireApproval   any                        `json:"require_approval,omitempty"`
	Headers           map[string]string          `json:"headers,omitempty"`
	VectorStoreIDs    []string                   `json:"vector_store_ids,omitempty"`
	Container         any                        `json:"container,omitempty"`
	Environment       any                        `json:"environment,omitempty"`
	Filters           any                        `json:"filters,omitempty"`
	MaxNumResults     *int                       `json:"max_num_results,omitempty"`
	RankingOptions    *FileSearchRankingOptions  `json:"ranking_options,omitempty"`
	RewriteQuery      *bool                      `json:"rewrite_query,omitempty"`
	SearchContextSize string                     `json:"search_context_size,omitempty"`
	UserLocation      *ResponseWebSearchLocation `json:"user_location,omitempty"`
	Format            *ResponseCustomToolFormat  `json:"format,omitempty"`
	Action            string                     `json:"action,omitempty"`
	Background        string                     `json:"background,omitempty"`
	InputFidelity     string                     `json:"input_fidelity,omitempty"`
	InputImageMask    *ResponseInputImageMask    `json:"input_image_mask,omitempty"`
	Model             string                     `json:"model,omitempty"`
	Moderation        string                     `json:"moderation,omitempty"`
	OutputCompression *int                       `json:"output_compression,omitempty"`
	OutputFormat      string                     `json:"output_format,omitempty"`
	PartialImages     *int                       `json:"partial_images,omitempty"`
	Quality           string                     `json:"quality,omitempty"`
	Size              string                     `json:"size,omitempty"`
}

type ResponseCustomToolFormat struct {
	Type       string `json:"type"`
	Syntax     string `json:"syntax,omitempty"`
	Definition string `json:"definition,omitempty"`
}

type ResponseInputImageMask struct {
	FileID   string `json:"file_id,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type ResponseWebSearchLocation struct {
	Type     string `json:"type"`
	City     string `json:"city,omitempty"`
	Country  string `json:"country,omitempty"`
	Region   string `json:"region,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}

type FileSearchRankingOptions struct {
	Ranker         string                  `json:"ranker,omitempty"`
	ScoreThreshold *float64                `json:"score_threshold,omitempty"`
	HybridSearch   *FileSearchHybridSearch `json:"hybrid_search,omitempty"`
}

type FileSearchHybridSearch struct {
	EmbeddingWeight *float64 `json:"embedding_weight,omitempty"`
	TextWeight      *float64 `json:"text_weight,omitempty"`
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
	ServiceTier         string                     `json:"service_tier,omitempty"`
	Conversation        *ResponseConversation      `json:"conversation,omitempty"`
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
	Phase               *string                 `json:"phase,omitempty"`
	EncryptedContent    *string                 `json:"encrypted_content,omitempty"`
	Action              json.RawMessage         `json:"action,omitempty"`
	Operation           json.RawMessage         `json:"operation,omitempty"`
	Environment         json.RawMessage         `json:"environment,omitempty"`
	Caller              json.RawMessage         `json:"caller,omitempty"`
	Actions             []json.RawMessage       `json:"actions,omitempty"`
	PendingSafetyChecks []json.RawMessage       `json:"pending_safety_checks,omitempty"`
	Outputs             []json.RawMessage       `json:"outputs,omitempty"`
	Results             []json.RawMessage       `json:"results,omitempty"`
	Tools               []json.RawMessage       `json:"tools,omitempty"`
	Output              json.RawMessage         `json:"output,omitempty"`
	Error               json.RawMessage         `json:"error,omitempty"`
	ID                  string                  `json:"id,omitempty"`
	Type                string                  `json:"type"`
	Status              string                  `json:"status,omitempty"`
	Role                string                  `json:"role,omitempty"`
	Name                string                  `json:"name,omitempty"`
	CallID              string                  `json:"call_id,omitempty"`
	CreatedBy           string                  `json:"created_by,omitempty"`
	MaxOutputLength     *int                    `json:"max_output_length,omitempty"`
	ContainerID         string                  `json:"container_id,omitempty"`
	ServerLabel         string                  `json:"server_label,omitempty"`
	ApprovalRequestID   string                  `json:"approval_request_id,omitempty"`
	Code                string                  `json:"code,omitempty"`
	Arguments           string                  `json:"arguments,omitempty"`
	Input               string                  `json:"input,omitempty"`
	Result              json.RawMessage         `json:"result,omitempty"`
	Content             []ResponseOutputContent `json:"content,omitempty"`
	Summary             []ResponseOutputContent `json:"summary,omitempty"`
}

type ResponseOutputContent struct {
	Logprobs    []TokenLogprob    `json:"logprobs,omitempty"`
	Annotations []json.RawMessage `json:"annotations,omitempty"`
	Refusal     string            `json:"refusal,omitempty"`
	Type        string            `json:"type"`
	Text        string            `json:"text,omitempty"`
}

type ResponseUsage struct {
	ProviderCostUSDTicks   *int64                  `json:"-"`
	NumSourcesUsed         *int                    `json:"num_sources_used,omitempty"`
	NumServerSideToolsUsed *int                    `json:"num_server_side_tools_used,omitempty"`
	OutputTokensDetails    *CompletionTokenDetails `json:"output_tokens_details,omitempty"`
	InputTokens            int                     `json:"input_tokens,omitempty"`
	OutputTokens           int                     `json:"output_tokens,omitempty"`
	TotalTokens            int                     `json:"total_tokens,omitempty"`
	InputTokensDetails     *InputTokenDetails      `json:"input_tokens_details,omitempty"`
}

func (u *ResponseUsage) UnmarshalJSON(data []byte) error {
	type responseUsage ResponseUsage
	var wire struct {
		responseUsage
		ProviderCostUSDTicks *int64 `json:"cost_in_usd_ticks"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*u = ResponseUsage(wire.responseUsage)
	u.ProviderCostUSDTicks = wire.ProviderCostUSDTicks
	return nil
}

type InputTokenDetails struct {
	CachedTokens        int `json:"cached_tokens,omitempty"`
	CacheWriteTokens    int `json:"cache_write_tokens,omitempty"`
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"`
	AudioTokens         int `json:"audio_tokens,omitempty"`
	ImageTokens         int `json:"image_tokens,omitempty"`
	ReasoningTokens     int `json:"reasoning_tokens,omitempty"`
	TextTokens          int `json:"text_tokens,omitempty"`
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
