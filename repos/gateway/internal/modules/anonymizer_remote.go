package modules

import (
	"context"
	"errors"
	"net/http"

	"ai-gateway-gateway/internal/openai"
)

type AnonymizeRequest struct {
	RequestID    string           `json:"request_id,omitempty"`
	Messages     []openai.Message `json:"messages,omitempty"`
	Input        any              `json:"input,omitempty"`
	Instructions string           `json:"instructions,omitempty"`
	Query        string           `json:"query,omitempty"`
	Documents    []any            `json:"documents,omitempty"`
	Keywords     []string         `json:"keywords,omitempty"`
	SpeakerNames []string         `json:"speaker_names,omitempty"`
}

type AnonymizeResponse struct {
	Messages     []openai.Message  `json:"messages,omitempty"`
	Input        any               `json:"input,omitempty"`
	Instructions string            `json:"instructions,omitempty"`
	Query        string            `json:"query,omitempty"`
	Documents    []any             `json:"documents,omitempty"`
	Keywords     []string          `json:"keywords,omitempty"`
	SpeakerNames []string          `json:"speaker_names,omitempty"`
	Replacements map[string]string `json:"replacements,omitempty"`
}

type RemoteAnonymizerModule struct {
	required bool
	endpoint string
	client   *http.Client
}

func NewRemoteAnonymizerModule(required bool, endpoint string) RemoteAnonymizerModule {
	return RemoteAnonymizerModule{required: required, endpoint: endpoint, client: newRemoteHTTPClient()}
}

func (m RemoteAnonymizerModule) Name() string   { return "anonymizer" }
func (m RemoteAnonymizerModule) Required() bool { return m.required }

func (m RemoteAnonymizerModule) Handle(ctx context.Context, req *RequestContext) error {
	request := AnonymizeRequest{RequestID: req.RequestID, Messages: projectMessages(req.Request.Messages)}
	if req.ResponseRequest != nil {
		request.Input = openai.TextOnlyProjection(req.ResponseRequest.Input)
		request.Instructions = req.ResponseRequest.Instructions
	}
	if req.EmbeddingRequest != nil {
		request.Input = openai.TextOnlyProjection(req.EmbeddingRequest.Input)
	}
	if req.RerankRequest != nil {
		request.Query = req.RerankRequest.Query
		request.Documents = make([]any, len(req.RerankRequest.Documents))
		for index, document := range req.RerankRequest.Documents {
			request.Documents[index] = openai.TextOnlyProjection(document)
		}
	}
	if req.ModerationRequest != nil {
		request.Input = openai.TextOnlyProjection(req.ModerationRequest.Input)
	}
	if req.ImageGenerationRequest != nil {
		request.Input = req.ImageGenerationRequest.Prompt
	}
	if req.ImageEditRequest != nil {
		request.Input = req.ImageEditRequest.Prompt
	}
	if req.AudioTranscriptionRequest != nil {
		if req.AudioTranscriptionRequest.Prompt != "" {
			request.Input = req.AudioTranscriptionRequest.Prompt
		}
		request.Keywords = append([]string(nil), req.AudioTranscriptionRequest.Keywords...)
		request.SpeakerNames = append([]string(nil), req.AudioTranscriptionRequest.KnownSpeakerNames...)
	}
	response, err := callRemote[AnonymizeRequest, AnonymizeResponse](ctx, m.client, m.endpoint, request)
	if err != nil {
		return err
	}
	mergedMessages, err := mergeProjectedMessages(req.Request.Messages, response.Messages)
	if err != nil {
		return err
	}
	req.Request.Messages = mergedMessages
	if req.ResponseRequest != nil {
		req.ResponseRequest.Input = openai.MergeTextProjection(req.ResponseRequest.Input, response.Input)
		req.ResponseRequest.Instructions = response.Instructions
	}
	if req.EmbeddingRequest != nil {
		req.EmbeddingRequest.Input = openai.MergeTextProjection(req.EmbeddingRequest.Input, response.Input)
	}
	if req.RerankRequest != nil {
		if len(response.Documents) != len(req.RerankRequest.Documents) {
			return errors.New("anonymizer returned an invalid rerank document projection")
		}
		req.RerankRequest.Query = response.Query
		for index := range req.RerankRequest.Documents {
			req.RerankRequest.Documents[index] = openai.MergeTextProjection(req.RerankRequest.Documents[index], response.Documents[index])
		}
	}
	if req.ModerationRequest != nil {
		req.ModerationRequest.Input = openai.MergeTextProjection(req.ModerationRequest.Input, response.Input)
	}
	if req.ImageGenerationRequest != nil {
		prompt, ok := response.Input.(string)
		if !ok {
			return errors.New("anonymizer returned an invalid image prompt projection")
		}
		req.ImageGenerationRequest.Prompt = prompt
	}
	if req.ImageEditRequest != nil {
		prompt, ok := response.Input.(string)
		if !ok {
			return errors.New("anonymizer returned an invalid image edit prompt projection")
		}
		req.ImageEditRequest.Prompt = prompt
	}
	if req.AudioTranscriptionRequest != nil {
		if req.AudioTranscriptionRequest.Prompt != "" {
			prompt, ok := response.Input.(string)
			if !ok {
				return errors.New("anonymizer returned an invalid transcription prompt projection")
			}
			req.AudioTranscriptionRequest.Prompt = prompt
		}
		if len(response.Keywords) != len(req.AudioTranscriptionRequest.Keywords) {
			return errors.New("anonymizer returned an invalid transcription keyword projection")
		}
		req.AudioTranscriptionRequest.Keywords = append([]string(nil), response.Keywords...)
		if len(response.SpeakerNames) != len(req.AudioTranscriptionRequest.KnownSpeakerNames) {
			return errors.New("anonymizer returned an invalid known speaker name projection")
		}
		req.AudioTranscriptionRequest.KnownSpeakerNames = append([]string(nil), response.SpeakerNames...)
	}
	req.AnonymizationValues = cloneStringMap(response.Replacements)
	return nil
}

func projectMessages(messages []openai.Message) []openai.Message {
	projected := make([]openai.Message, len(messages))
	for index, message := range messages {
		projected[index] = message
		projected[index].Content = openai.TextOnlyProjection(message.Content)
		projected[index].ToolCalls = append([]openai.ToolCall(nil), message.ToolCalls...)
	}
	return projected
}

func mergeProjectedMessages(original []openai.Message, transformed []openai.Message) ([]openai.Message, error) {
	if len(original) != len(transformed) {
		return nil, errors.New("anonymizer returned an invalid message projection")
	}
	merged := make([]openai.Message, len(original))
	for index := range original {
		merged[index] = original[index]
		merged[index].Content = openai.MergeTextProjection(original[index].Content, transformed[index].Content)
		if len(original[index].ToolCalls) != len(transformed[index].ToolCalls) {
			return nil, errors.New("anonymizer returned an invalid tool-call projection")
		}
		merged[index].ToolCalls = append([]openai.ToolCall(nil), original[index].ToolCalls...)
		for callIndex := range merged[index].ToolCalls {
			merged[index].ToolCalls[callIndex].Function.Arguments = transformed[index].ToolCalls[callIndex].Function.Arguments
		}
	}
	return merged, nil
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
