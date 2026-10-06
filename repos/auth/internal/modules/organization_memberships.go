package modules

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// OrganizationMembership is an explicit operator approval. Global directory
// roles and IdP claims cannot grant administration of an organization.
type OrganizationMembership struct {
	OrganizationID string    `json:"organization_id"`
	UserID         string    `json:"user_id"`
	Roles          []string  `json:"roles"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type OrganizationMembershipPage struct {
	Data   []OrganizationMembership `json:"data"`
	Total  int                      `json:"total"`
	Offset int                      `json:"offset"`
	Limit  int                      `json:"limit"`
}

type organizationMembershipStore interface {
	PutOrganizationMembership(context.Context, OrganizationMembership) (OrganizationMembership, error)
	ListOrganizationMemberships(context.Context, string, int, int) (OrganizationMembershipPage, error)
}

func (m AuthModule) PutOrganizationMembership(ctx context.Context, member OrganizationMembership) (OrganizationMembership, error) {
	if !validDirectoryID(member.OrganizationID) || !validDirectoryID(member.UserID) || !validDirectoryStatus(member.Status) || len(member.Roles) == 0 || len(member.Roles) > 3 {
		return OrganizationMembership{}, ErrInvalidDirectoryEntry
	}
	roles := slices.Clone(member.Roles)
	slices.Sort(roles)
	for i, role := range roles {
		if (role != "user" && role != "developer" && role != "org_admin") || (i > 0 && role == roles[i-1]) {
			return OrganizationMembership{}, ErrInvalidDirectoryEntry
		}
	}
	member.Roles = roles
	store, ok := m.store.(organizationMembershipStore)
	if !ok {
		return OrganizationMembership{}, ErrJWTDirectoryUnavailable
	}
	return store.PutOrganizationMembership(ctx, member)
}

func (m AuthModule) ListOrganizationMemberships(ctx context.Context, org string, offset, limit int) (OrganizationMembershipPage, error) {
	if !validDirectoryID(org) || offset < 0 || offset > 1000000 || limit < 1 || limit > 500 {
		return OrganizationMembershipPage{}, ErrInvalidDirectoryEntry
	}
	store, ok := m.store.(organizationMembershipStore)
	if !ok {
		return OrganizationMembershipPage{}, ErrJWTDirectoryUnavailable
	}
	return store.ListOrganizationMemberships(ctx, org, offset, limit)
}

func (s *PostgresVirtualKeyStore) PutOrganizationMembership(ctx context.Context, member OrganizationMembership) (OrganizationMembership, error) {
	err := s.pool.QueryRow(ctx, `INSERT INTO auth_organization_memberships(organization_id,user_id,roles,status) VALUES($1,$2,$3,$4)
	ON CONFLICT(organization_id,user_id) DO UPDATE SET roles=EXCLUDED.roles,status=EXCLUDED.status,updated_at=now()
	RETURNING created_at,updated_at`, member.OrganizationID, member.UserID, member.Roles, member.Status).Scan(&member.CreatedAt, &member.UpdatedAt)
	if isPrincipalForeignKeyViolation(err) {
		return OrganizationMembership{}, ErrInvalidDirectoryEntry
	}
	return member, err
}

func (s *PostgresVirtualKeyStore) ListOrganizationMemberships(ctx context.Context, org string, offset, limit int) (OrganizationMembershipPage, error) {
	page := OrganizationMembershipPage{Data: []OrganizationMembership{}, Offset: offset, Limit: limit}
	// Count and page use one snapshot, including an offset beyond the last row.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_organizations WHERE id=$1)`, org).Scan(&exists); err != nil {
		return page, err
	}
	if !exists {
		return page, ErrDirectoryNotFound
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM auth_organization_memberships WHERE organization_id=$1`, org).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := tx.Query(ctx, `SELECT organization_id,user_id,roles,status,created_at,updated_at FROM auth_organization_memberships WHERE organization_id=$1 ORDER BY user_id LIMIT $2 OFFSET $3`, org, limit, offset)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var member OrganizationMembership
		if err = rows.Scan(&member.OrganizationID, &member.UserID, &member.Roles, &member.Status, &member.CreatedAt, &member.UpdatedAt); err != nil {
			return page, err
		}
		page.Data = append(page.Data, member)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	return page, tx.Commit(ctx)
}

func principalRoleApproved(p authorizedJWTPrincipal, role string) bool {
	switch role {
	case "org_admin":
		return p.OrganizationID != "" && slices.Contains(p.OrganizationRoles, role)
	case "team_admin":
		return p.TeamID != "" && slices.Contains(p.Roles, role)
	case "user", "developer":
		return slices.Contains(p.Roles, role) && (len(p.OrganizationRoles) == 0 || slices.Contains(p.OrganizationRoles, role))
	default:
		return slices.Contains(p.Roles, role)
	}
}
