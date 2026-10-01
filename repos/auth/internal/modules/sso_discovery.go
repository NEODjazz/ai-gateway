package modules

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

type SSODiscovery struct {
	Issuer           string `json:"issuer"`
	AuthorizationURL string `json:"authorization_url"`
	TokenURL         string `json:"token_url"`
	JWKSURL          string `json:"jwks_url"`
}

func DiscoverSSO(ctx context.Context, issuer string) (SSODiscovery, error) {
	u, valid := ssoEndpoint(issuer)
	if !valid {
		return SSODiscovery{}, ErrSSOConfiguration
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/.well-known/openid-configuration"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return SSODiscovery{}, ErrSSOConfiguration
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return SSODiscovery{}, ErrSSOUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return SSODiscovery{}, ErrSSOConfiguration
	}
	var metadata struct {
		Issuer        string   `json:"issuer"`
		Authorization string   `json:"authorization_endpoint"`
		Token         string   `json:"token_endpoint"`
		JWKS          string   `json:"jwks_uri"`
		PKCE          []string `json:"code_challenge_methods_supported"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	if decoder.Decode(&metadata) != nil || decoder.Decode(&struct{}{}) != io.EOF || metadata.Issuer != issuer {
		return SSODiscovery{}, ErrSSOConfiguration
	}
	if len(metadata.PKCE) > 0 {
		found := false
		for _, method := range metadata.PKCE {
			found = found || method == "S256"
		}
		if !found {
			return SSODiscovery{}, ErrSSOConfiguration
		}
	}
	for _, raw := range []string{metadata.Authorization, metadata.Token, metadata.JWKS} {
		endpoint, ok := ssoEndpoint(raw)
		original, _ := ssoEndpoint(issuer)
		if !ok || endpoint.Scheme != original.Scheme || !strings.EqualFold(endpoint.Host, original.Host) {
			return SSODiscovery{}, ErrSSOConfiguration
		}
	}
	return SSODiscovery{Issuer: issuer, AuthorizationURL: metadata.Authorization, TokenURL: metadata.Token, JWKSURL: metadata.JWKS}, nil
}
