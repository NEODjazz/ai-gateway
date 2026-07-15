package modules

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type RemoteModule struct {
	name         string
	required     bool
	endpoint     string
	postResponse bool
	client       *http.Client
}

func NewRemoteModule(name string, required bool, endpoint string) RemoteModule {
	return RemoteModule{
		name:     name,
		required: required,
		endpoint: endpoint,
		client: &http.Client{
			Timeout: 2 * time.Second,
		},
	}
}

func NewRemotePostResponseModule(name string, required bool, endpoint string) RemoteModule {
	module := NewRemoteModule(name, required, endpoint)
	module.postResponse = true
	return module
}

func (m RemoteModule) Name() string {
	return m.name
}

func (m RemoteModule) Required() bool {
	return m.required
}

func (m RemoteModule) PostResponseEnabled() bool {
	return m.postResponse
}

func (m RemoteModule) Handle(ctx context.Context, req *RequestContext) error {
	return m.call(ctx, req)
}

func (m RemoteModule) HandlePostResponse(ctx context.Context, req *RequestContext) error {
	return m.call(ctx, req)
}

func (m RemoteModule) call(ctx context.Context, req *RequestContext) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode == http.StatusUnavailableForLegalReasons {
		return ErrContentRejected
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("remote %s returned %s", m.name, resp.Status)
	}

	return json.NewDecoder(resp.Body).Decode(req)
}
