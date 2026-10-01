package modules

import (
	"context"
	"errors"
	"strings"
)

type JWTIdentity struct {
	Issuer       string `json:"issuer"`
	Subject      string `json:"subject"`
	Audience     string `json:"audience"`
	PolicyDigest string `json:"policy_digest"`
}

var ErrBackgroundAuthorizationUnavailable = errors.New("background identity authorization unavailable")

type BackgroundAuthorizer interface {
	ReauthorizeBackground(context.Context, RequestContext) error
}

// Legacy jobs preserve their existing behavior. Directory jobs fail closed if
// their reference or the authenticated reauthorization service is unavailable.
func (p Pipeline) ReauthorizeBackground(ctx context.Context, req RequestContext) error {
	if req.JWTIdentity == nil {
		if req.ModelAccessRestricted && strings.HasPrefix(req.CredentialID, "jwt:") {
			return ErrUnauthorized
		}
		return nil
	}
	for _, m := range p.modules {
		if m.Name() == "auth" {
			if client, ok := m.(BackgroundAuthorizer); ok {
				return client.ReauthorizeBackground(ctx, req)
			}
		}
	}
	return ErrBackgroundAuthorizationUnavailable
}
