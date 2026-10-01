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
	JWTIdentity           *JWTIdentity `json:"jwt_identity,omitempty"`
	UserID                string       `json:"user_id"`
	Roles                 []string     `json:"roles,omitempty"`
	CredentialID          string       `json:"credential_id,omitempty"`
	CredentialAlias       string       `json:"credential_alias,omitempty"`
	TeamID                string       `json:"team_id,omitempty"`
	OrganizationID        string       `json:"organization_id,omitempty"`
	Tags                  []string     `json:"tags,omitempty"`
	AccessGroupIDs        []string     `json:"access_group_ids,omitempty"`
	ModelAccessRestricted bool         `json:"model_access_restricted,omitempty"`
	ToolAccessRestricted  bool         `json:"tool_access_restricted,omitempty"`
	AllowedModels         []string     `json:"allowed_models,omitempty"`
	AllowedTools          []string     `json:"allowed_tools,omitempty"`
	RateLimitRPM          int          `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM          int          `json:"rate_limit_tpm,omitempty"`
}

type RemoteAuthModule struct {
	required          bool
	endpoint          string
	client            *http.Client
	reauthorizeURL    string
	reauthorizeSecret string
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
	req.JWTIdentity = response.JWTIdentity
	req.UserID = response.UserID
	req.Roles = append([]string(nil), response.Roles...)
	req.CredentialID = response.CredentialID
	req.CredentialAlias = response.CredentialAlias
	req.TeamID = response.TeamID
	req.OrganizationID = response.OrganizationID
	req.Tags = append([]string(nil), response.Tags...)
	req.AccessGroupIDs = append([]string(nil), response.AccessGroupIDs...)
	req.ModelAccessRestricted = response.ModelAccessRestricted
	req.ToolAccessRestricted = response.ToolAccessRestricted
	req.AllowedModels = append([]string(nil), response.AllowedModels...)
	req.AllowedTools = append([]string(nil), response.AllowedTools...)
	req.RateLimitRPM = response.RateLimitRPM
	req.RateLimitTPM = response.RateLimitTPM
	req.APIKey = ""
	return nil
}

func (m RemoteAuthModule) WithJWTReauthorization(baseURL, secret string) RemoteAuthModule {
	m.reauthorizeURL = endpoint(baseURL, "/internal/v1/jwt-principals:reauthorize")
	m.reauthorizeSecret = secret
	return m
}
func (m RemoteAuthModule) ReauthorizeBackground(ctx context.Context, req RequestContext) error {
	if m.reauthorizeSecret == "" || m.reauthorizeURL == "" {
		return ErrBackgroundAuthorizationUnavailable
	}
	input := struct {
		Identity     *JWTIdentity `json:"jwt_identity"`
		UserID       string       `json:"user_id"`
		CredentialID string       `json:"credential_id"`
		Roles        []string     `json:"roles"`
	}{req.JWTIdentity, req.UserID, req.CredentialID, req.Roles}
	result, err := callRemoteWithHeaders[any, struct {
		Authorized bool `json:"authorized"`
	}](ctx, m.client, m.reauthorizeURL, input, map[string]string{
		"X-Management-Token": m.reauthorizeSecret, "X-Request-ID": req.RequestID, "X-Actor-ID": req.UserID, "X-Actor-Credential-ID": req.CredentialID,
	})
	if err != nil {
		return err
	}
	if !result.Authorized {
		return ErrUnauthorized
	}
	return nil
}
