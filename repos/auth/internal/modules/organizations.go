package modules

import (
	"context"
	"errors"
	"strings"
	"time"
)

type Organization struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status"`
	TeamIDs     []string  `json:"team_ids"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type organizationStore interface {
	ListOrganizations(context.Context, int) ([]Organization, error)
	PutOrganization(context.Context, Organization) (Organization, error)
	PutOrganizationTeam(context.Context, string, string) (Organization, error)
}

func (m AuthModule) organizationStore() (organizationStore, error) {
	if m.initErr != nil {
		return nil, m.initErr
	}
	store, ok := m.store.(organizationStore)
	if !ok || store == nil {
		return nil, errors.New("persistent organization directory is unavailable")
	}
	return store, nil
}

func (m AuthModule) ListOrganizations(ctx context.Context, limit int) ([]Organization, error) {
	store, err := m.organizationStore()
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 500 {
		return nil, ErrInvalidDirectoryEntry
	}
	return store.ListOrganizations(ctx, limit)
}

func (m AuthModule) PutOrganization(ctx context.Context, organization Organization) (Organization, error) {
	store, err := m.organizationStore()
	if err != nil {
		return Organization{}, err
	}
	organization.ID, organization.Name, organization.Description = strings.TrimSpace(organization.ID), strings.TrimSpace(organization.Name), strings.TrimSpace(organization.Description)
	if !validDirectoryID(organization.ID) || organization.Name == "" || len(organization.Name) > 256 || len(organization.Description) > 1024 || !validDirectoryStatus(organization.Status) {
		return Organization{}, ErrInvalidDirectoryEntry
	}
	return store.PutOrganization(ctx, organization)
}

func (m AuthModule) PutOrganizationTeam(ctx context.Context, organizationID, teamID string) (Organization, error) {
	store, err := m.organizationStore()
	if err != nil {
		return Organization{}, err
	}
	organizationID, teamID = strings.TrimSpace(organizationID), strings.TrimSpace(teamID)
	if !validDirectoryID(organizationID) || !validDirectoryID(teamID) {
		return Organization{}, ErrInvalidDirectoryEntry
	}
	return store.PutOrganizationTeam(ctx, organizationID, teamID)
}

func (s *PostgresVirtualKeyStore) ListOrganizations(ctx context.Context, limit int) ([]Organization, error) {
	rows, err := s.pool.Query(ctx, `SELECT o.id,o.name,o.description,o.status,
		COALESCE(array_agg(ot.team_id ORDER BY ot.team_id) FILTER (WHERE ot.team_id IS NOT NULL),'{}'),o.created_at,o.updated_at
		FROM auth_organizations o LEFT JOIN auth_organization_teams ot ON ot.organization_id=o.id
		GROUP BY o.id ORDER BY o.created_at DESC,o.id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Organization, 0)
	for rows.Next() {
		var organization Organization
		if err := rows.Scan(&organization.ID, &organization.Name, &organization.Description, &organization.Status, &organization.TeamIDs, &organization.CreatedAt, &organization.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, organization)
	}
	return result, rows.Err()
}

func (s *PostgresVirtualKeyStore) PutOrganization(ctx context.Context, organization Organization) (Organization, error) {
	err := s.pool.QueryRow(ctx, `INSERT INTO auth_organizations(id,name,description,status) VALUES($1,$2,$3,$4)
		ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name,description=EXCLUDED.description,status=EXCLUDED.status,updated_at=now()
		RETURNING id,name,description,status,created_at,updated_at`, organization.ID, organization.Name, organization.Description, organization.Status).Scan(&organization.ID, &organization.Name, &organization.Description, &organization.Status, &organization.CreatedAt, &organization.UpdatedAt)
	organization.TeamIDs = []string{}
	return organization, err
}

func (s *PostgresVirtualKeyStore) PutOrganizationTeam(ctx context.Context, organizationID, teamID string) (Organization, error) {
	if _, err := s.pool.Exec(ctx, `INSERT INTO auth_organization_teams(organization_id,team_id) VALUES($1,$2)
		ON CONFLICT(team_id) DO UPDATE SET organization_id=EXCLUDED.organization_id`, organizationID, teamID); err != nil {
		return Organization{}, err
	}
	rows, err := s.ListOrganizations(ctx, 500)
	if err != nil {
		return Organization{}, err
	}
	for _, organization := range rows {
		if organization.ID == organizationID {
			return organization, nil
		}
	}
	return Organization{}, errors.New("organization not found")
}
