package modules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	return applyJWTPrincipal(req, principal, roles)
}

func applyJWTPrincipal(req *RequestContext, principal authorizedJWTPrincipal, roles []string) error {
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
	principal.Roles = roles
	payload, err := json.Marshal(principal)
	if err != nil {
		return ErrJWTDirectoryUnavailable
	}
	digest := sha256.Sum256(payload)
	req.JWTIdentity = &JWTIdentity{Issuer: principal.Issuer, Subject: principal.Subject, Audience: principal.Audience, PolicyDigest: hex.EncodeToString(digest[:])}
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

// JWTPrincipalPage contains operator-visible policies, including disabled bindings.
type JWTPrincipalPage struct {
	Data   []JWTPrincipalPolicy `json:"data"`
	Total  int                  `json:"total"`
	Limit  int                  `json:"limit"`
	Offset int                  `json:"offset"`
}

type jwtPrincipalManagementStore interface {
	PutJWTPrincipal(context.Context, JWTPrincipalPolicy) (JWTPrincipalPolicy, error)
	ListJWTPrincipals(context.Context, string, int, int) (JWTPrincipalPage, error)
}

func (m AuthModule) PutJWTPrincipal(ctx context.Context, policy JWTPrincipalPolicy) (JWTPrincipalPolicy, error) {
	if !validJWTIdentityValue(policy.Issuer, 2048) || !validJWTIdentityValue(policy.Subject, 256) || !validJWTIdentityValue(policy.Audience, 256) || !validDirectoryID(policy.UserID) || (policy.TeamID != "" && !validDirectoryID(policy.TeamID)) || !validPolicyStrings(policy.Tags) || !validPolicyStrings(policy.AllowedModels) || !validPolicyStrings(policy.AllowedTools) || !validAccessGroupIDs(policy.AccessGroupIDs) || policy.RateLimitRPM < 0 || policy.RateLimitRPM > 2147483647 || policy.RateLimitTPM < 0 || policy.RateLimitTPM > 2147483647 {
		return JWTPrincipalPolicy{}, ErrInvalidDirectoryEntry
	}
	for _, values := range [][]string{policy.Tags, policy.AllowedModels, policy.AllowedTools} {
		for _, value := range values {
			if !validJWTIdentityValue(value, 256) {
				return JWTPrincipalPolicy{}, ErrInvalidDirectoryEntry
			}
		}
	}
	store, ok := m.store.(jwtPrincipalManagementStore)
	if !ok {
		return JWTPrincipalPolicy{}, ErrJWTDirectoryUnavailable
	}
	return store.PutJWTPrincipal(ctx, policy)
}

func (m AuthModule) ListJWTPrincipals(ctx context.Context, userID string, offset, limit int) (JWTPrincipalPage, error) {
	if (userID != "" && !validDirectoryID(userID)) || offset < 0 || limit < 1 || limit > 500 {
		return JWTPrincipalPage{}, ErrInvalidDirectoryEntry
	}
	store, ok := m.store.(jwtPrincipalManagementStore)
	if !ok {
		return JWTPrincipalPage{}, ErrJWTDirectoryUnavailable
	}
	return store.ListJWTPrincipals(ctx, userID, offset, limit)
}

func (s *PostgresVirtualKeyStore) PutJWTPrincipal(ctx context.Context, p JWTPrincipalPolicy) (JWTPrincipalPolicy, error) {
	// User ownership is immutable, including after disabling a binding. No delete
	// endpoint is provided: revocation must not permit later identity reassignment.
	tag, err := s.pool.Exec(ctx, `INSERT INTO auth_jwt_principals
 (issuer,subject,audience,user_id,team_id,tags,access_group_ids,allowed_models,allowed_tools,rate_limit_rpm,rate_limit_tpm,enabled)
 VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9,$10,$11,$12)
 ON CONFLICT(issuer,subject,audience) DO UPDATE SET team_id=EXCLUDED.team_id,tags=EXCLUDED.tags,
 access_group_ids=EXCLUDED.access_group_ids,allowed_models=EXCLUDED.allowed_models,allowed_tools=EXCLUDED.allowed_tools,
 rate_limit_rpm=EXCLUDED.rate_limit_rpm,rate_limit_tpm=EXCLUDED.rate_limit_tpm,enabled=EXCLUDED.enabled,updated_at=now()
 WHERE auth_jwt_principals.user_id=EXCLUDED.user_id`, p.Issuer, p.Subject, p.Audience, p.UserID, p.TeamID,
		nonNilStrings(p.Tags), nonNilStrings(p.AccessGroupIDs), nonNilStrings(p.AllowedModels), nonNilStrings(p.AllowedTools), p.RateLimitRPM, p.RateLimitTPM, p.Enabled)
	if isPrincipalForeignKeyViolation(err) {
		return JWTPrincipalPolicy{}, ErrInvalidDirectoryEntry
	}
	if err != nil {
		return JWTPrincipalPolicy{}, err
	}
	if tag.RowsAffected() != 1 {
		return JWTPrincipalPolicy{}, ErrDirectoryConflict
	}
	return p, nil
}

func (s *PostgresVirtualKeyStore) ListJWTPrincipals(ctx context.Context, userID string, offset, limit int) (JWTPrincipalPage, error) {
	page := JWTPrincipalPage{Data: []JWTPrincipalPolicy{}, Offset: offset, Limit: limit}
	// A repeatable-read transaction keeps totals and the page consistent.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM auth_jwt_principals WHERE ($1='' OR user_id=$1)`, userID).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := tx.Query(ctx, `SELECT issuer,subject,audience,user_id,COALESCE(team_id,''),tags,access_group_ids,allowed_models,allowed_tools,rate_limit_rpm,rate_limit_tpm,enabled FROM auth_jwt_principals WHERE ($1='' OR user_id=$1) ORDER BY issuer,audience,subject LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var p JWTPrincipalPolicy
		if err = rows.Scan(&p.Issuer, &p.Subject, &p.Audience, &p.UserID, &p.TeamID, &p.Tags, &p.AccessGroupIDs, &p.AllowedModels, &p.AllowedTools, &p.RateLimitRPM, &p.RateLimitTPM, &p.Enabled); err != nil {
			return page, err
		}
		page.Data = append(page.Data, p)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	return page, tx.Commit(ctx)
}

func isPrincipalForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// ReauthorizeJWTPrincipal is only exposed over the authenticated internal
// management channel. Its identity and roles come from a previously authorized
// durable job, never from a public request header or provider metadata.
func (m AuthModule) ReauthorizeJWTPrincipal(ctx context.Context, req *RequestContext) error {
	if m.sso != nil {
		current, err := m.currentJWTModule(ctx)
		if err != nil {
			return err
		}
		return current.ReauthorizeJWTPrincipal(ctx, req)
	}
	ref := req.JWTIdentity
	if ref == nil || m.jwtConfig.IdentityMode != "directory" || ref.Issuer != m.jwtConfig.Issuer || ref.Audience != m.jwtConfig.Audience || !validJWTIdentityValue(ref.Subject, 256) || len(ref.PolicyDigest) != 64 || len(req.Roles) == 0 || req.CredentialID != jwtPrincipalCredentialID(ref.Issuer, ref.Audience, ref.Subject) {
		return ErrUnauthorized
	}
	store, ok := m.store.(jwtPrincipalStore)
	if !ok {
		return ErrJWTDirectoryUnavailable
	}
	p, found, err := store.LookupJWTPrincipal(ctx, ref.Issuer, ref.Subject, ref.Audience)
	if err != nil {
		return ErrJWTDirectoryUnavailable
	}
	if !found || !p.Enabled || p.UserID != req.UserID {
		return ErrUnauthorized
	}
	roles := []string{}
	for _, role := range req.Roles {
		mapped := false
		for _, target := range m.jwtConfig.RoleMappings {
			if target == role {
				mapped = true
				break
			}
		}
		if mapped && slices.Contains(p.Roles, role) && !slices.Contains(roles, role) && (role != "team_admin" || p.TeamID != "") {
			roles = append(roles, role)
		}
	}
	if len(roles) == 0 {
		return ErrUnauthorized
	}
	fresh := RequestContext{}
	if err := applyJWTPrincipal(&fresh, p, roles); err != nil {
		return err
	}
	if fresh.JWTIdentity.PolicyDigest != ref.PolicyDigest {
		return ErrUnauthorized
	}
	return nil
}
