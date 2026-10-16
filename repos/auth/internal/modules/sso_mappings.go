package modules

import (
	"slices"
)

type SSOVerifiedIdentity struct {
	Issuer         string   `json:"issuer"`
	Subject        string   `json:"subject"`
	Audience       string   `json:"audience"`
	UserID         string   `json:"user_id"`
	OrganizationID string   `json:"organization_id,omitempty"`
	Roles          []string `json:"roles"`
	VerifiedAt     int64    `json:"verified_at"`
	Approved       bool     `json:"approved"`
}

func (p SSOProfileConfig) browserJWTConfig() JWTAuthConfig {
	config := p.jwtConfig()
	config.RoleMappings = make(map[string]string, len(p.RoleMappings)+len(p.GroupMappings))
	for external, role := range p.RoleMappings {
		config.RoleMappings[external] = role
	}
	// These names only supply the role target set for proof reauthorization.
	// Browser authorization maps the two verified claims separately below.
	for _, role := range p.GroupMappings {
		key := "oidc-group-target:" + role
		for {
			if _, exists := config.RoleMappings[key]; !exists {
				break
			}
			key = "_" + key
		}
		config.RoleMappings[key] = role
	}

	return config
}

func verifiedSSOClaimStrings(claims map[string]any, path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	value := claimValue(claims, path)
	if value == nil {
		return nil, nil
	}
	var values []any
	switch typed := value.(type) {
	case string:
		values = []any{typed}
	case []any:
		values = typed
	default:
		return nil, ErrUnauthorized
	}
	if len(values) > 256 {
		return nil, ErrUnauthorized
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok || !validJWTIdentityValue(text, 256) {
			return nil, ErrUnauthorized
		}
		result = append(result, text)
	}
	return result, nil
}

func (p SSOProfileConfig) mappedBrowserRoles(claims map[string]any) ([]any, error) {
	result := []any{}
	for _, source := range []struct {
		path     string
		mappings map[string]string
	}{{p.RolesClaim, p.RoleMappings}, {p.GroupsClaim, p.GroupMappings}} {
		values, err := verifiedSSOClaimStrings(claims, source.path)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			if role := source.mappings[value]; role != "" && !slices.Contains(result, any(role)) {
				result = append(result, role)
			}
		}
	}
	return result, nil
}

func (p SSOProfileConfig) allowsBrowserRole(role string) bool {
	for _, mappings := range []map[string]string{p.RoleMappings, p.GroupMappings} {
		for _, target := range mappings {
			if target == role {
				return true
			}
		}
	}
	return false
}
