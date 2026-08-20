package modules

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

type AuthRequest struct {
	Token string `json:"token"`
}

type AuthResponse struct {
	UserID        string   `json:"user_id"`
	Roles         []string `json:"roles,omitempty"`
	CredentialID  string   `json:"credential_id,omitempty"`
	TeamID        string   `json:"team_id,omitempty"`
	AllowedModels []string `json:"allowed_models,omitempty"`
	RateLimitRPM  int      `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM  int      `json:"rate_limit_tpm,omitempty"`
}

type RemoteAuthModule struct {
	required bool
	endpoint string
	client   *http.Client
}

func NewRemoteAuthModule(required bool, endpoint string) RemoteAuthModule {
	return RemoteAuthModule{required: required, endpoint: endpoint, client: newRemoteHTTPClient()}
}

func (m RemoteAuthModule) Name() string   { return "auth" }
func (m RemoteAuthModule) Required() bool { return m.required }

func (m RemoteAuthModule) Handle(ctx context.Context, req *RequestContext) error {
	response, err := callRemote[AuthRequest, AuthResponse](ctx, m.client, m.endpoint, AuthRequest{Token: req.APIKey})
	if err != nil {
		return err
	}
	if strings.TrimSpace(response.UserID) == "" {
		return errors.New("auth response is missing user_id")
	}
	req.UserID = response.UserID
	req.Roles = append([]string(nil), response.Roles...)
	req.CredentialID = response.CredentialID
	req.TeamID = response.TeamID
	req.AllowedModels = append([]string(nil), response.AllowedModels...)
	req.RateLimitRPM = response.RateLimitRPM
	req.RateLimitTPM = response.RateLimitTPM
	req.APIKey = ""
	return nil
}
