package modules

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

type provisionedGroupStore interface {
	GetTeamWithMembers(context.Context, string) (DirectoryTeam, []string, error)
	FindTeam(context.Context, string, string) (DirectoryTeam, bool, error)
	SaveTeamWithMembers(context.Context, DirectoryTeam, []string, bool) (DirectoryTeam, []string, error)
}

func (m AuthModule) provisionedGroupStore() (provisionedGroupStore, error) {
	if m.initErr != nil {
		return nil, m.initErr
	}
	store, ok := m.store.(provisionedGroupStore)
	if !ok || store == nil {
		return nil, errors.New("persistent group directory is unavailable")
	}
	return store, nil
}

func (m AuthModule) GetDirectoryTeam(ctx context.Context, id string) (DirectoryTeam, []string, error) {
	store, err := m.provisionedGroupStore()
	if err != nil {
		return DirectoryTeam{}, nil, err
	}
	id = strings.TrimSpace(id)
	if !validDirectoryID(id) {
		return DirectoryTeam{}, nil, ErrInvalidDirectoryEntry
	}
	return store.GetTeamWithMembers(ctx, id)
}

func (m AuthModule) FindDirectoryTeam(ctx context.Context, attribute, value string) (DirectoryTeam, bool, error) {
	store, err := m.provisionedGroupStore()
	if err != nil {
		return DirectoryTeam{}, false, err
	}
	attribute, value = strings.TrimSpace(attribute), strings.TrimSpace(value)
	if (attribute != "displayName" && attribute != "externalId") || value == "" || len(value) > 256 {
		return DirectoryTeam{}, false, ErrInvalidDirectoryEntry
	}
	return store.FindTeam(ctx, attribute, value)
}

func (m AuthModule) SaveDirectoryTeamMembers(ctx context.Context, team DirectoryTeam, userIDs []string, create bool) (DirectoryTeam, []string, error) {
	store, err := m.provisionedGroupStore()
	if err != nil {
		return DirectoryTeam{}, nil, err
	}
	team.ID, team.ExternalID, team.Name, team.Description = strings.TrimSpace(team.ID), strings.TrimSpace(team.ExternalID), strings.TrimSpace(team.Name), strings.TrimSpace(team.Description)
	if !validDirectoryID(team.ID) || len(team.ExternalID) > 256 || team.Name == "" || len(team.Name) > 256 || len(team.Description) > 1024 || !validDirectoryStatus(team.Status) || len(userIDs) > 500 {
		return DirectoryTeam{}, nil, ErrInvalidDirectoryEntry
	}
	seen := make(map[string]struct{}, len(userIDs))
	clean := make([]string, 0, len(userIDs))
	for _, userID := range userIDs {
		userID = strings.TrimSpace(userID)
		if !validDirectoryID(userID) {
			return DirectoryTeam{}, nil, ErrInvalidDirectoryEntry
		}
		if _, exists := seen[userID]; exists {
			continue
		}
		seen[userID] = struct{}{}
		clean = append(clean, userID)
	}
	team, clean, err = store.SaveTeamWithMembers(ctx, team, clean, create)
	if isUniqueViolation(err) {
		return DirectoryTeam{}, nil, ErrDirectoryConflict
	}
	return team, clean, err
}

func (s *PostgresVirtualKeyStore) GetTeamWithMembers(ctx context.Context, id string) (DirectoryTeam, []string, error) {
	var team DirectoryTeam
	var members []string
	err := s.pool.QueryRow(ctx, `SELECT t.id,t.external_id,t.name,t.description,t.status,t.created_at,t.updated_at,t.scim_deleted_at,
		COALESCE(array_agg(m.user_id ORDER BY m.user_id) FILTER (WHERE m.user_id IS NOT NULL),'{}')
		FROM auth_teams t LEFT JOIN auth_team_memberships m ON m.team_id=t.id WHERE t.id=$1 GROUP BY t.id`, id).
		Scan(&team.ID, &team.ExternalID, &team.Name, &team.Description, &team.Status, &team.CreatedAt, &team.UpdatedAt, &team.DeletedAt, &members)
	if errors.Is(err, pgx.ErrNoRows) {
		return DirectoryTeam{}, nil, ErrDirectoryNotFound
	}
	if err != nil {
		return DirectoryTeam{}, nil, err
	}
	team.MemberCount = len(members)
	return team, members, nil
}

func (s *PostgresVirtualKeyStore) FindTeam(ctx context.Context, attribute, value string) (DirectoryTeam, bool, error) {
	query := `SELECT id,external_id,name,description,status,created_at,updated_at,scim_deleted_at FROM auth_teams WHERE external_id=$1 AND scim_deleted_at IS NULL`
	switch attribute {
	case "displayName":
		query = `SELECT id,external_id,name,description,status,created_at,updated_at,scim_deleted_at FROM auth_teams WHERE lower(name)=$1 AND scim_deleted_at IS NULL`
		value = strings.ToLower(value)
	case "externalId":
	default:
		return DirectoryTeam{}, false, ErrInvalidDirectoryEntry
	}
	var team DirectoryTeam
	err := s.pool.QueryRow(ctx, query, value).Scan(&team.ID, &team.ExternalID, &team.Name, &team.Description, &team.Status, &team.CreatedAt, &team.UpdatedAt, &team.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return DirectoryTeam{}, false, nil
	}
	return team, err == nil, err
}

func (s *PostgresVirtualKeyStore) SaveTeamWithMembers(ctx context.Context, team DirectoryTeam, userIDs []string, create bool) (DirectoryTeam, []string, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DirectoryTeam{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if create {
		err = tx.QueryRow(ctx, `INSERT INTO auth_teams(id,external_id,name,description,status,scim_deleted_at) VALUES($1,$2,$3,$4,$5,$6)
			ON CONFLICT DO NOTHING RETURNING id,external_id,name,description,status,created_at,updated_at,scim_deleted_at`, team.ID, team.ExternalID, team.Name, team.Description, team.Status, team.DeletedAt).
			Scan(&team.ID, &team.ExternalID, &team.Name, &team.Description, &team.Status, &team.CreatedAt, &team.UpdatedAt, &team.DeletedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return DirectoryTeam{}, nil, ErrDirectoryConflict
		}
	} else {
		err = tx.QueryRow(ctx, `UPDATE auth_teams SET external_id=$2,name=$3,description=$4,status=$5,scim_deleted_at=$6,updated_at=now()
			WHERE id=$1 AND scim_deleted_at IS NULL RETURNING id,external_id,name,description,status,created_at,updated_at,scim_deleted_at`, team.ID, team.ExternalID, team.Name, team.Description, team.Status, team.DeletedAt).
			Scan(&team.ID, &team.ExternalID, &team.Name, &team.Description, &team.Status, &team.CreatedAt, &team.UpdatedAt, &team.DeletedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return DirectoryTeam{}, nil, ErrDirectoryNotFound
		}
	}
	if err != nil {
		return DirectoryTeam{}, nil, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM auth_team_memberships WHERE team_id=$1 AND NOT (user_id=ANY($2))`, team.ID, userIDs); err != nil {
		return DirectoryTeam{}, nil, err
	}
	for _, userID := range userIDs {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND scim_deleted_at IS NULL)`, userID).Scan(&exists); err != nil {
			return DirectoryTeam{}, nil, err
		}
		if !exists {
			return DirectoryTeam{}, nil, ErrDirectoryNotFound
		}
		if _, err := tx.Exec(ctx, `INSERT INTO auth_team_memberships(team_id,user_id,roles) VALUES($1,$2,'{}') ON CONFLICT(team_id,user_id) DO NOTHING`, team.ID, userID); err != nil {
			return DirectoryTeam{}, nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return DirectoryTeam{}, nil, err
	}
	team.MemberCount = len(userIDs)
	return team, append([]string(nil), userIDs...), nil
}
