package modules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

type ProviderRemoteModule struct {
	name        string
	required    bool
	metadataKey string
	endpoint    string
	client      *http.Client
}

func NewProviderRemoteModule(name string, required bool, endpoint string) ProviderRemoteModule {
	return ProviderRemoteModule{
		name:        name,
		required:    required,
		metadataKey: "provider.modules." + name + ".enabled",
		endpoint:    endpoint,
		client:      newRemoteHTTPClient(),
	}
}

func (m ProviderRemoteModule) Name() string {
	return m.name
}

func (m ProviderRemoteModule) Required() bool {
	return m.required
}

func (m ProviderRemoteModule) PostResponseEnabled() bool {
	return m.name == "dlp"
}

func (m ProviderRemoteModule) HandlePostResponse(ctx context.Context, req *RequestContext) error {
	if !metadataBool(req.Metadata, "provider.modules.dlp.output_enabled") {
		return nil
	}
	payload, err := scanResponsePayload(req)
	if err != nil {
		return errors.Join(ErrGuardrailUnavailable, err)
	}
	if payload == "" {
		return nil
	}
	if strings.TrimSpace(m.endpoint) == "" {
		return errors.Join(ErrGuardrailUnavailable, errors.New("remote module url is empty"))
	}
	response, err := callRemote[ScanRequest, ScanResponse](ctx, m.client, m.endpoint, ScanRequest{RequestID: req.RequestID, Content: payload})
	if err != nil {
		if errors.Is(err, ErrContentRejected) {
			return err
		}
		return errors.Join(ErrGuardrailUnavailable, err)
	}
	if !response.Allowed {
		return ErrContentRejected
	}
	return nil
}

func (m ProviderRemoteModule) Handle(ctx context.Context, req *RequestContext) error {
	if !metadataBool(req.Metadata, m.metadataKey) {
		return nil
	}
	if strings.TrimSpace(m.endpoint) == "" {
		return m.policyError(req, errors.New("remote module url is empty"))
	}
	request := ScanRequest{
		RequestID: req.RequestID,
		Content:   scanPayload(req),
	}
	if m.name == "av" {
		attachments, err := requestImageAttachments(req)
		if err != nil {
			return err
		}
		request.Attachments = attachments
	}
	response, err := callRemote[ScanRequest, ScanResponse](ctx, m.client, m.endpoint, request)
	if err != nil {
		if m.name == "av" && len(request.Attachments) > 0 && !errors.Is(err, ErrContentRejected) {
			return errors.Join(ErrGuardrailUnavailable, err)
		}
		return m.policyError(req, err)
	}
	if !response.Allowed {
		return ErrContentRejected
	}
	return nil
}

func (m ProviderRemoteModule) policyError(req *RequestContext, err error) error {
	if metadataBool(req.Metadata, "policy.guardrail.required") {
		return errors.Join(ErrGuardrailUnavailable, err)
	}
	return err
}

type ScanRequest struct {
	RequestID   string                   `json:"request_id,omitempty"`
	Content     string                   `json:"content"`
	Attachments []openai.ImageAttachment `json:"attachments,omitempty"`
}

func requestImageAttachments(req *RequestContext) ([]openai.ImageAttachment, error) {
	attachments, err := openai.ChatImageAttachments(req.Request.Messages)
	if err != nil {
		return nil, err
	}
	if req.ImageEditRequest != nil {
		attachments = append(attachments, req.ImageEditRequest.Images...)
		if req.ImageEditRequest.Mask != nil {
			attachments = append(attachments, *req.ImageEditRequest.Mask)
		}
		if err := openai.ValidateImageAttachments(attachments); err != nil {
			return nil, err
		}
	}
	if req.ImageVariationRequest != nil {
		attachments = append(attachments, req.ImageVariationRequest.Image)
		if err := openai.ValidateImageAttachments(attachments); err != nil {
			return nil, err
		}
	}
	if req.AudioTranscriptionRequest != nil {
		if err := openai.ValidateAudioAttachment(req.AudioTranscriptionRequest.File); err != nil {
			return nil, err
		}
		attachments = append(attachments, openai.ImageAttachment{MediaType: req.AudioTranscriptionRequest.File.MediaType, Data: req.AudioTranscriptionRequest.File.Data})
		if err := openai.ValidateKnownSpeakerReferences(req.AudioTranscriptionRequest.KnownSpeakerReferences); err != nil {
			return nil, err
		}
		for _, reference := range req.AudioTranscriptionRequest.KnownSpeakerReferences {
			attachments = append(attachments, openai.ImageAttachment{MediaType: reference.MediaType, Data: reference.Data})
		}
	}
	if req.OCRRequest != nil {
		attachment, err := req.OCRRequest.Document.Attachment()
		if err != nil {
			return nil, err
		}
		if attachment != nil {
			attachments = append(attachments, *attachment)
		}
	}
	documents, err := openai.BedrockDocumentAttachments(req.Request.Messages)
	if err != nil {
		return nil, err
	}
	attachments = append(attachments, documents...)
	if req.ResponseRequest == nil {
		if req.ModerationRequest == nil {
			return attachments, nil
		}
		moderationAttachments, err := openai.ModerationImageAttachments(req.ModerationRequest.Input)
		if err != nil {
			return nil, err
		}
		return append(attachments, moderationAttachments...), nil
	}
	responseAttachments, err := openai.ResponseImageAttachments(req.ResponseRequest.Input)
	if err != nil {
		return nil, err
	}
	attachments = append(attachments, responseAttachments...)
	responseAudio, err := openai.ResponseAudioAttachments(req.ResponseRequest.Input)
	if err != nil {
		return nil, err
	}
	for _, audio := range responseAudio {
		attachments = append(attachments, openai.ImageAttachment{MediaType: audio.MediaType, Data: audio.Data})
	}
	responseFiles, err := openai.ResponseFileAttachments(req.ResponseRequest.Input)
	if err != nil {
		return nil, err
	}
	for _, file := range responseFiles {
		attachments = append(attachments, openai.ImageAttachment{MediaType: file.MediaType, Data: file.Data})
	}
	if req.ModerationRequest != nil {
		moderationAttachments, err := openai.ModerationImageAttachments(req.ModerationRequest.Input)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, moderationAttachments...)
	}
	return attachments, nil
}

type ScanResponse struct {
	Allowed bool `json:"allowed"`
}

func scanPayload(req *RequestContext) string {
	var parts []string
	for _, message := range req.Request.Messages {
		if text := openai.ContentText(message.Content); text != "" {
			parts = append(parts, message.Role+": "+text)
		}
		for _, call := range message.ToolCalls {
			if call.Function.Arguments != "" {
				parts = append(parts, "tool_arguments: "+call.Function.Arguments)
			}
		}
		for _, block := range message.Reasoning {
			if block.Thinking != "" {
				parts = append(parts, "reasoning: "+block.Thinking)
			}
		}
	}
	if text := openai.BedrockDocumentText(req.Request.Messages); text != "" {
		parts = append(parts, "document: "+text)
	}
	if len(req.Request.BedrockRequestMetadata) > 0 {
		keys := make([]string, 0, len(req.Request.BedrockRequestMetadata))
		for key := range req.Request.BedrockRequestMetadata {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			parts = append(parts, "request_metadata: "+key+"="+req.Request.BedrockRequestMetadata[key])
		}
	}
	if len(req.Request.BedrockAdditionalModelRequestFields) > 0 {
		parts = append(parts, "additional_model_request_fields: "+string(req.Request.BedrockAdditionalModelRequestFields))
	}
	if req.ResponseRequest != nil {
		if req.ResponseRequest.Instructions != "" {
			parts = append(parts, "instructions: "+req.ResponseRequest.Instructions)
		}
		if text := openai.ContentText(req.ResponseRequest.Input); text != "" {
			parts = append(parts, "input: "+text)
		}
	}
	if req.EmbeddingRequest != nil {
		if text := openai.EmbeddingInputText(req.EmbeddingRequest.Input); text != "" {
			parts = append(parts, "embedding_input: "+text)
		}
	}
	if req.RerankRequest != nil {
		if text, ok := openai.RerankDocumentText(*req.RerankRequest); ok {
			parts = append(parts, "rerank: "+text)
		}
	}
	if req.ModerationRequest != nil {
		if text := openai.ModerationInputText(req.ModerationRequest.Input); text != "" {
			parts = append(parts, "moderation_input: "+text)
		}
	}
	if req.ImageGenerationRequest != nil && req.ImageGenerationRequest.Prompt != "" {
		parts = append(parts, "image_prompt: "+req.ImageGenerationRequest.Prompt)
	}
	if req.ImageEditRequest != nil && req.ImageEditRequest.Prompt != "" {
		parts = append(parts, "image_edit_prompt: "+req.ImageEditRequest.Prompt)
	}
	if req.AudioTranscriptionRequest != nil {
		if req.AudioTranscriptionRequest.Prompt != "" {
			parts = append(parts, "transcription_prompt: "+req.AudioTranscriptionRequest.Prompt)
		}
		for _, keyword := range req.AudioTranscriptionRequest.Keywords {
			parts = append(parts, "transcription_keyword: "+keyword)
		}
		for _, name := range req.AudioTranscriptionRequest.KnownSpeakerNames {
			parts = append(parts, "known_speaker_name: "+name)
		}
	}
	if req.OCRRequest != nil && req.OCRRequest.DocumentAnnotationPrompt != "" {
		parts = append(parts, "ocr_annotation_prompt: "+req.OCRRequest.DocumentAnnotationPrompt)
	}
	return strings.Join(parts, "\n")
}

const maxResponseScanBytes = 64 << 10

func scanResponsePayload(req *RequestContext) (string, error) {
	var result strings.Builder
	appendText := func(label, value string) error {
		if value == "" {
			return nil
		}
		additional := len(label) + len(value)
		if result.Len() > 0 {
			additional++
		}
		if additional > maxResponseScanBytes-result.Len() {
			return errors.New("response text projection exceeds the 64 KiB DLP scan limit")
		}
		if result.Len() > 0 {
			result.WriteByte('\n')
		}
		result.WriteString(label)
		result.WriteString(value)
		return nil
	}
	if req.Response != nil {
		for _, choice := range req.Response.Choices {
			if err := appendText("assistant: ", openai.ContentText(choice.Message.Content)); err != nil {
				return "", err
			}
			for _, annotation := range choice.Message.Annotations {
				if annotation.SourceCitation == nil {
					continue
				}
				if err := appendText("citation_source: ", annotation.SourceCitation.Source); err != nil {
					return "", err
				}
				for _, content := range annotation.SourceCitation.SourceContent {
					if err := appendText("citation_content: ", content); err != nil {
						return "", err
					}
				}
			}
			if choice.Message.Refusal != nil {
				if err := appendText("refusal: ", *choice.Message.Refusal); err != nil {
					return "", err
				}
			}
			if choice.Message.Audio != nil && choice.Message.Audio.Transcript != nil {
				if err := appendText("audio_transcript: ", *choice.Message.Audio.Transcript); err != nil {
					return "", err
				}
			}
			if choice.Message.FunctionCall != nil {
				if err := appendText("function_arguments: ", choice.Message.FunctionCall.Arguments); err != nil {
					return "", err
				}
			}
			for _, call := range choice.Message.ToolCalls {
				if err := appendText("tool_arguments: ", call.Function.Arguments); err != nil {
					return "", err
				}
			}
			for _, block := range choice.Message.Reasoning {
				if err := appendText("reasoning: ", block.Thinking); err != nil {
					return "", err
				}
			}
			for _, block := range choice.Message.NativeContent {
				var value any
				if err := json.Unmarshal(block, &value); err != nil {
					return "", fmt.Errorf("decode native output: %w", err)
				}
				if err := appendText("native_output: ", nativeOutputText(value)); err != nil {
					return "", err
				}
			}
		}
	}
	if req.CompletionResponse != nil {
		for _, choice := range req.CompletionResponse.Choices {
			if err := appendText("completion: ", choice.Text); err != nil {
				return "", err
			}
		}
	}
	if req.ResponsesResponse != nil {
		if err := appendText("output_text: ", req.ResponsesResponse.OutputText); err != nil {
			return "", err
		}
		for _, item := range req.ResponsesResponse.Output {
			if err := appendText("arguments: ", item.Arguments); err != nil {
				return "", err
			}
			for _, content := range append(append([]openai.ResponseOutputContent(nil), item.Content...), item.Summary...) {
				if err := appendText("output: ", content.Text); err != nil {
					return "", err
				}
				if err := appendText("refusal: ", content.Refusal); err != nil {
					return "", err
				}
			}
		}
	}
	if req.CompactedResponse != nil {
		for _, item := range req.CompactedResponse.Output {
			var value any
			if json.Unmarshal(item, &value) == nil {
				if err := appendText("compacted_output: ", openai.ContentText(value)); err != nil {
					return "", err
				}
			}
		}
	}
	if req.ImageGenerationResponse != nil {
		for _, image := range req.ImageGenerationResponse.Data {
			if err := appendText("revised_prompt: ", image.RevisedPrompt); err != nil {
				return "", err
			}
		}
	}
	if req.AudioTranscriptionResponse != nil {
		if err := appendText("transcript: ", req.AudioTranscriptionResponse.Text); err != nil {
			return "", err
		}
	}
	if req.SearchResponse != nil {
		for _, item := range req.SearchResponse.Results {
			if err := appendText("search_title: ", item.Title); err != nil {
				return "", err
			}
			if err := appendText("search_snippet: ", item.Snippet); err != nil {
				return "", err
			}
		}
	}
	if req.OCRResponse != nil {
		if req.OCRResponse.DocumentAnnotation != nil {
			if err := appendText("document_annotation: ", *req.OCRResponse.DocumentAnnotation); err != nil {
				return "", err
			}
		}
		for _, page := range req.OCRResponse.Pages {
			var value any
			if json.Unmarshal(page, &value) == nil {
				if err := appendText("ocr_page: ", openai.ContentText(value)); err != nil {
					return "", err
				}
			}
		}
	}
	return result.String(), nil
}

func nativeOutputText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if part := nativeOutputText(item); part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, " ")
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			switch key {
			case "encrypted_content", "signature":
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			if part := nativeOutputText(typed[key]); part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func metadataBool(metadata map[string]string, key string) bool {
	switch strings.ToLower(strings.TrimSpace(metadata[key])) {
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
}
