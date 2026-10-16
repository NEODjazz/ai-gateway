package modules

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// OrganizationMetadata omits the unbounded legacy team_ids collection.
type OrganizationMetadata struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type TeamOrganization struct {
	TeamID       string                `json:"team_id"`
	Organization *OrganizationMetadata `json:"organization"`
}

type OrganizationPageQuery struct {
	Search string
	Status string
	Offset int
	Limit  int
}

func (q OrganizationPageQuery) Valid() bool {
	return len(q.Search) <= 256 && !strings.ContainsRune(q.Search, 0) && (q.Status == "" || validDirectoryStatus(q.Status)) && q.Offset >= 0 && q.Limit >= 1 && q.Limit <= 100
}

type OrganizationPage struct {
	Data   []OrganizationMetadata `json:"data"`
	Total  int                    `json:"total"`
	Offset int                    `json:"offset"`
	Limit  int                    `json:"limit"`
}

type teamOrganizationStore interface {
	TeamOrganization(context.Context, string) (TeamOrganization, error)
	SetTeamOrganization(context.Context, string, string) (TeamOrganization, error)
	ClearTeamOrganization(context.Context, string, string) (TeamOrganization, error)
	OrganizationPage(context.Context, OrganizationPageQuery) (OrganizationPage, error)
	CreateTeamInOrganization(context.Context, DirectoryTeam, string) (DirectoryTeam, error)
}

func (m AuthModule) teamOrganizationStore() (teamOrganizationStore, error) {
	if m.initErr != nil {
		return nil, m.initErr
	}
	s, ok := m.store.(teamOrganizationStore)
	if !ok {
		return nil, errors.New("team organization directory is unavailable")
	}
	return s, nil
}

func (m AuthModule) TeamOrganization(ctx context.Context, teamID string) (TeamOrganization, error) {
	if !validDirectoryID(teamID) {
		return TeamOrganization{}, ErrInvalidDirectoryEntry
	}
	s, err := m.teamOrganizationStore()
	if err != nil {
		return TeamOrganization{}, err
	}
	return s.TeamOrganization(ctx, teamID)
}

func (m AuthModule) ChangeTeamOrganization(ctx context.Context, teamID, orgID string, remove bool) (TeamOrganization, error) {
	if !validDirectoryID(teamID) || !validDirectoryID(orgID) {
		return TeamOrganization{}, ErrInvalidDirectoryEntry
	}
	s, err := m.teamOrganizationStore()
	if err != nil {
		return TeamOrganization{}, err
	}
	if remove {
		return s.ClearTeamOrganization(ctx, teamID, orgID)
	}
	return s.SetTeamOrganization(ctx, teamID, orgID)
}

func (m AuthModule) OrganizationPage(ctx context.Context, q OrganizationPageQuery) (OrganizationPage, error) {
	if !q.Valid() {
		return OrganizationPage{}, ErrInvalidDirectoryEntry
	}
	s, err := m.teamOrganizationStore()
	if err != nil {
		return OrganizationPage{}, err
	}
	return s.OrganizationPage(ctx, q)
}

func (m AuthModule) CreateTeamInOrganization(ctx context.Context, team DirectoryTeam, orgID string) (DirectoryTeam, error) {
	team, err := normalizeDirectoryTeam(team)
	if err != nil || team.DeletedAt != nil || !validDirectoryID(orgID) {
		return DirectoryTeam{}, ErrInvalidDirectoryEntry
	}
	s, err := m.teamOrganizationStore()
	if err != nil {
		return DirectoryTeam{}, err
	}
	team, err = s.CreateTeamInOrganization(ctx, team, orgID)
	if isUniqueViolation(err) {
		err = ErrDirectoryConflict
	}
	return team, err
}

const teamOrganizationSelect = `SELECT t.id,COALESCE(o.id,''),COALESCE(o.name,''),COALESCE(o.description,''),COALESCE(o.status,''),COALESCE(o.created_at,'epoch'::timestamptz),COALESCE(o.updated_at,'epoch'::timestamptz)
 FROM auth_teams t LEFT JOIN auth_organization_teams ot ON ot.team_id=t.id LEFT JOIN auth_organizations o ON o.id=ot.organization_id WHERE t.id=$1`

func scanTeamOrganization(row pgx.Row) (TeamOrganization, error) {
	var result TeamOrganization
	var organization OrganizationMetadata
	err := row.Scan(&result.TeamID, &organization.ID, &organization.Name, &organization.Description, &organization.Status, &organization.CreatedAt, &organization.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return TeamOrganization{}, ErrDirectoryNotFound
	}
	if organization.ID != "" {
		result.Organization = &organization
	}
	return result, err
}

func (s *PostgresVirtualKeyStore) TeamOrganization(ctx context.Context, teamID string) (TeamOrganization, error) {
	return scanTeamOrganization(s.pool.QueryRow(ctx, teamOrganizationSelect, teamID))
}

func (s *PostgresVirtualKeyStore) SetTeamOrganization(ctx context.Context, teamID, orgID string) (TeamOrganization, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return TeamOrganization{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ownerID string
	err = tx.QueryRow(ctx, `INSERT INTO auth_organization_teams(organization_id,team_id)
 SELECT o.id,t.id FROM auth_organizations o CROSS JOIN auth_teams t WHERE o.id=$1 AND t.id=$2 AND o.status='active' AND t.scim_deleted_at IS NULL
 ON CONFLICT(team_id) DO UPDATE SET organization_id=auth_organization_teams.organization_id RETURNING organization_id`, orgID, teamID).Scan(&ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return TeamOrganization{}, ErrInvalidDirectoryEntry
	}
	if err != nil {
		return TeamOrganization{}, err
	}
	if ownerID != orgID {
		return TeamOrganization{}, ErrOrganizationTeamConflict
	}
	result, err := scanTeamOrganization(tx.QueryRow(ctx, teamOrganizationSelect, teamID))
	if err != nil {
		return TeamOrganization{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return TeamOrganization{}, err
	}
	return result, nil
}

func (s *PostgresVirtualKeyStore) ClearTeamOrganization(ctx context.Context, teamID, orgID string) (TeamOrganization, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return TeamOrganization{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := tx.Exec(ctx, `DELETE FROM auth_organization_teams WHERE team_id=$1 AND organization_id=$2`, teamID, orgID)
	if err != nil {
		return TeamOrganization{}, err
	}
	if result.RowsAffected() != 1 {
		return TeamOrganization{}, ErrDirectoryNotFound
	}
	owner, err := scanTeamOrganization(tx.QueryRow(ctx, teamOrganizationSelect, teamID))
	if err != nil {
		return TeamOrganization{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return TeamOrganization{}, err
	}
	return owner, nil
}

func (s *PostgresVirtualKeyStore) OrganizationPage(ctx context.Context, q OrganizationPageQuery) (OrganizationPage, error) {
	if !q.Valid() {
		return OrganizationPage{}, ErrInvalidDirectoryEntry
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return OrganizationPage{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	const where = ` FROM auth_organizations WHERE ($1='' OR status=$1) AND ($2='' OR strpos(lower(id || ' ' || name || ' ' || description),lower($2))>0)`
	page := OrganizationPage{Data: make([]OrganizationMetadata, 0), Offset: q.Offset, Limit: q.Limit}
	if err = tx.QueryRow(ctx, `SELECT count(*)`+where, q.Status, q.Search).Scan(&page.Total); err != nil {
		return OrganizationPage{}, err
	}
	rows, err := tx.Query(ctx, `SELECT id,name,description,status,created_at,updated_at`+where+` ORDER BY lower(name),id OFFSET $3 LIMIT $4`, q.Status, q.Search, q.Offset, q.Limit)
	if err != nil {
		return OrganizationPage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item OrganizationMetadata
		if err = rows.Scan(&item.ID, &item.Name, &item.Description, &item.Status, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return OrganizationPage{}, err
		}
		page.Data = append(page.Data, item)
	}
	if err = rows.Err(); err != nil {
		return OrganizationPage{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return OrganizationPage{}, err
	}
	return page, nil
}

func (s *PostgresVirtualKeyStore) CreateTeamInOrganization(ctx context.Context, team DirectoryTeam, orgID string) (DirectoryTeam, error) {
	team, _, err := s.saveTeamWithMembers(ctx, team, []string{}, true, orgID)
	return team, err
}
