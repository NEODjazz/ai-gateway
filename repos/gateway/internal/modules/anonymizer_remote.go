package modules

import (
	"context"
	"net/http"

	"ai-gateway-gateway/internal/openai"
)

type AnonymizeRequest struct {
	RequestID    string           `json:"request_id,omitempty"`
	Messages     []openai.Message `json:"messages,omitempty"`
	Input        any              `json:"input,omitempty"`
	Instructions string           `json:"instructions,omitempty"`
}

type AnonymizeResponse struct {
	Messages     []openai.Message  `json:"messages,omitempty"`
	Input        any               `json:"input,omitempty"`
	Instructions string            `json:"instructions,omitempty"`
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
	request := AnonymizeRequest{RequestID: req.RequestID, Messages: req.Request.Messages}
	if req.ResponseRequest != nil {
		request.Input = req.ResponseRequest.Input
		request.Instructions = req.ResponseRequest.Instructions
	}
	response, err := callRemote[AnonymizeRequest, AnonymizeResponse](ctx, m.client, m.endpoint, request)
	if err != nil {
		return err
	}
	req.Request.Messages = response.Messages
	if req.ResponseRequest != nil {
		req.ResponseRequest.Input = response.Input
		req.ResponseRequest.Instructions = response.Instructions
	}
	req.AnonymizationValues = cloneStringMap(response.Replacements)
	return nil
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
