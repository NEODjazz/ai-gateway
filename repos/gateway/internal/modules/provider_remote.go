package modules

import (
	"context"
	"errors"
	"net/http"
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
	return strings.Join(parts, "\n")
}

func metadataBool(metadata map[string]string, key string) bool {
	switch strings.ToLower(strings.TrimSpace(metadata[key])) {
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
}
