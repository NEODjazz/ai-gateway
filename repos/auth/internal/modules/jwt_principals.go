package modules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
)

var ErrJWTDirectoryUnavailable = errors.New("jwt identity directory unavailable")

// JWTPrincipalPolicy is an explicit operator-managed identity binding. Empty
// model/tool grants deny access; a wildcard must be assigned intentionally.
type JWTPrincipalPolicy struct {
	Issuer         string   `json:"issuer"`
	Subject        string   `json:"subject"`
	Audience       string   `json:"audience"`
	UserID         string   `json:"user_id"`
	TeamID         string   `json:"team_id,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	AccessGroupIDs []string `json:"access_group_ids,omitempty"`
	AllowedModels  []string `json:"allowed_models,omitempty"`
	AllowedTools   []string `json:"allowed_tools,omitempty"`
	RateLimitRPM   int      `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM   int      `json:"rate_limit_tpm,omitempty"`
	Enabled        bool     `json:"enabled"`
}

type authorizedJWTPrincipal struct {
	JWTPrincipalPolicy
	OrganizationID string
	Roles          []string
}

type jwtPrincipalStore interface {
	LookupJWTPrincipal(context.Context, string, string, string) (authorizedJWTPrincipal, bool, error)
	JWTPrincipalsReady(context.Context) error
}

func jwtPrincipalCredentialID(issuer, audience, subject string) string {
	// Identity validation excludes NUL, so these boundaries are unambiguous.
	digest := sha256.Sum256([]byte(issuer + "\x00" + audience + "\x00" + subject))
	return "jwt:" + hex.EncodeToString(digest[:])
}

func validJWTIdentityValue(value string, limit int) bool {
	if value == "" || len(value) > limit || strings.TrimSpace(value) != value {
		return false
	}
	return !strings.ContainsFunc(value, unicode.IsControl)
}

func (m AuthModule) authorizeJWTPrincipal(ctx context.Context, req *RequestContext, claims jwtClaims) error {
	if !validJWTIdentityValue(claims.Issuer, 2048) || !validJWTIdentityValue(claims.Subject, 256) {
		return ErrUnauthorized
	}
	store, ok := m.store.(jwtPrincipalStore)
	if !ok {
		return ErrJWTDirectoryUnavailable
	}
	principal, found, err := store.LookupJWTPrincipal(ctx, claims.Issuer, claims.Subject, m.jwtConfig.Audience)
	if err != nil {
		return fmt.Errorf("%w: principal lookup failed", ErrJWTDirectoryUnavailable)
	}
	if !found || !principal.Enabled || principal.UserID == "" || principal.Issuer != claims.Issuer || principal.Subject != claims.Subject || principal.Audience != m.jwtConfig.Audience {
		return ErrUnauthorized
	}
	if team := claimString(claims.Raw, m.jwtConfig.TeamIDClaim); team != "" && team != principal.TeamID {
		return ErrUnauthorized
	}
	roles := make([]string, 0)
	for _, external := range claimStrings(claims.Raw, m.jwtConfig.RolesClaim) {
		role := m.jwtConfig.RoleMappings[external]
		if role != "" && slices.Contains(principal.Roles, role) && !slices.Contains(roles, role) {
			if role == "team_admin" && principal.TeamID == "" {
				continue
			}
			roles = append(roles, role)
		}
	}
	if len(roles) == 0 {
		return ErrUnauthorized
	}
	slices.Sort(roles)
	req.UserID, req.TeamID, req.OrganizationID = principal.UserID, principal.TeamID, principal.OrganizationID
	req.CredentialID = jwtPrincipalCredentialID(principal.Issuer, principal.Audience, principal.Subject)
	req.CredentialAlias = ""
	req.Roles = roles
	req.Tags = append([]string(nil), principal.Tags...)
	req.AccessGroupIDs = append([]string(nil), principal.AccessGroupIDs...)
	req.AllowedModels = append([]string(nil), principal.AllowedModels...)
	req.AllowedTools = append([]string(nil), principal.AllowedTools...)
	req.ModelAccessRestricted, req.ToolAccessRestricted = true, true
	req.RateLimitRPM, req.RateLimitTPM = principal.RateLimitRPM, principal.RateLimitTPM
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	req.Metadata["auth.method"], req.Metadata["auth.issuer"] = "jwt_directory", principal.Issuer
	return nil
}

func (s *PostgresVirtualKeyStore) JWTPrincipalsReady(ctx context.Context) error {
	var exists bool
	if s == nil || s.pool == nil {
		return ErrJWTDirectoryUnavailable
	}
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass('public.auth_jwt_principals') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		return ErrJWTDirectoryUnavailable
	}
	return nil
}

func (s *PostgresVirtualKeyStore) LookupJWTPrincipal(ctx context.Context, issuer, subject, audience string) (authorizedJWTPrincipal, bool, error) {
	var principal authorizedJWTPrincipal
	if s == nil || s.pool == nil {
		return principal, false, ErrJWTDirectoryUnavailable
	}
	err := s.pool.QueryRow(ctx, `
		SELECT p.issuer,p.subject,p.audience,p.user_id,COALESCE(p.team_id,''),
		       p.tags,p.access_group_ids,p.allowed_models,p.allowed_tools,p.rate_limit_rpm,p.rate_limit_tpm,p.enabled,
		       COALESCE(ot.organization_id,''),u.roles
		FROM auth_jwt_principals p JOIN users u ON u.id=p.user_id
		LEFT JOIN auth_teams t ON t.id=p.team_id
		LEFT JOIN auth_team_memberships tm ON tm.team_id=p.team_id AND tm.user_id=p.user_id
		LEFT JOIN auth_organization_teams ot ON ot.team_id=p.team_id
		LEFT JOIN auth_organizations o ON o.id=ot.organization_id
		WHERE p.issuer=$1 AND p.subject=$2 AND p.audience=$3 AND p.enabled
		  AND u.status='active' AND u.scim_deleted_at IS NULL
		  AND (p.team_id IS NULL OR (t.status='active' AND t.scim_deleted_at IS NULL AND tm.user_id IS NOT NULL))
		  AND (ot.organization_id IS NULL OR o.status='active')`, issuer, subject, audience).Scan(
		&principal.Issuer, &principal.Subject, &principal.Audience, &principal.UserID, &principal.TeamID,
		&principal.Tags, &principal.AccessGroupIDs, &principal.AllowedModels, &principal.AllowedTools,
		&principal.RateLimitRPM, &principal.RateLimitTPM, &principal.Enabled, &principal.OrganizationID, &principal.Roles)
	if errors.Is(err, pgx.ErrNoRows) {
		return authorizedJWTPrincipal{}, false, nil
	}
	return principal, err == nil, err
}
