package modules

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type DirectoryUser struct {
	ID        string    `json:"id"`
	Email     string    `json:"email,omitempty"`
	Name      string    `json:"name,omitempty"`
	Status    string    `json:"status"`
	Roles     []string  `json:"roles,omitempty"`
	TeamIDs   []string  `json:"team_ids,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type DirectoryTeam struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status"`
	MemberCount int       `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type TeamMembership struct {
	TeamID    string    `json:"team_id"`
	UserID    string    `json:"user_id"`
	Roles     []string  `json:"roles,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

var ErrInvalidDirectoryEntry = errors.New("invalid identity directory entry")

func (m AuthModule) ListDirectoryUsers(ctx context.Context, teamID string, offset, limit int) ([]DirectoryUser, int, error) {
	store, err := m.directoryStore()
	if err != nil {
		return nil, 0, err
	}
	if offset < 0 || limit < 1 || limit > 500 || len(teamID) > 256 {
		return nil, 0, ErrInvalidDirectoryEntry
	}
	return store.ListUsers(ctx, strings.TrimSpace(teamID), offset, limit)
}

func (m AuthModule) PutDirectoryUser(ctx context.Context, user DirectoryUser) (DirectoryUser, error) {
	store, err := m.directoryStore()
	if err != nil {
		return DirectoryUser{}, err
	}
	user.ID, user.Email, user.Name = strings.TrimSpace(user.ID), strings.TrimSpace(user.Email), strings.TrimSpace(user.Name)
	if !validDirectoryID(user.ID) || len(user.Email) > 320 || len(user.Name) > 256 || !validDirectoryStatus(user.Status) || !validPolicyStrings(user.Roles) {
		return DirectoryUser{}, ErrInvalidDirectoryEntry
	}
	return store.PutUser(ctx, user)
}

func (m AuthModule) ListDirectoryTeams(ctx context.Context, teamID string, offset, limit int) ([]DirectoryTeam, int, error) {
	store, err := m.directoryStore()
	if err != nil {
		return nil, 0, err
	}
	if offset < 0 || limit < 1 || limit > 500 || len(teamID) > 256 {
		return nil, 0, ErrInvalidDirectoryEntry
	}
	return store.ListTeams(ctx, strings.TrimSpace(teamID), offset, limit)
}

func (m AuthModule) PutDirectoryTeam(ctx context.Context, team DirectoryTeam) (DirectoryTeam, error) {
	store, err := m.directoryStore()
	if err != nil {
		return DirectoryTeam{}, err
	}
	team.ID, team.Name, team.Description = strings.TrimSpace(team.ID), strings.TrimSpace(team.Name), strings.TrimSpace(team.Description)
	if !validDirectoryID(team.ID) || team.Name == "" || len(team.Name) > 256 || len(team.Description) > 1024 || !validDirectoryStatus(team.Status) {
		return DirectoryTeam{}, ErrInvalidDirectoryEntry
	}
	return store.PutTeam(ctx, team)
}

func (m AuthModule) PutTeamMembership(ctx context.Context, membership TeamMembership) (TeamMembership, error) {
	store, err := m.directoryStore()
	if err != nil {
		return TeamMembership{}, err
	}
	membership.TeamID, membership.UserID = strings.TrimSpace(membership.TeamID), strings.TrimSpace(membership.UserID)
	if !validDirectoryID(membership.TeamID) || !validDirectoryID(membership.UserID) || !validPolicyStrings(membership.Roles) {
		return TeamMembership{}, ErrInvalidDirectoryEntry
	}
	return store.PutMembership(ctx, membership)
}

func (m AuthModule) ListTeamMemberships(ctx context.Context, teamID string, limit int) ([]TeamMembership, error) {
	store, err := m.teamMembershipStore()
	if err != nil {
		return nil, err
	}
	teamID = strings.TrimSpace(teamID)
	if !validDirectoryID(teamID) || limit < 1 || limit > 500 {
		return nil, ErrInvalidDirectoryEntry
	}
	return store.ListMemberships(ctx, teamID, limit)
}

func (m AuthModule) DeleteTeamMembership(ctx context.Context, teamID, userID string) (bool, error) {
	store, err := m.teamMembershipStore()
	if err != nil {
		return false, err
	}
	teamID, userID = strings.TrimSpace(teamID), strings.TrimSpace(userID)
	if !validDirectoryID(teamID) || !validDirectoryID(userID) {
		return false, ErrInvalidDirectoryEntry
	}
	return store.DeleteMembership(ctx, teamID, userID)
}

type teamMembershipStore interface {
	ListMemberships(context.Context, string, int) ([]TeamMembership, error)
	DeleteMembership(context.Context, string, string) (bool, error)
}

func (m AuthModule) teamMembershipStore() (teamMembershipStore, error) {
	if m.initErr != nil {
		return nil, m.initErr
	}
	store, ok := m.store.(teamMembershipStore)
	if !ok || store == nil {
		return nil, errors.New("persistent team membership directory is unavailable")
	}
	return store, nil
}

type identityDirectoryStore interface {
	ListUsers(context.Context, string, int, int) ([]DirectoryUser, int, error)
	PutUser(context.Context, DirectoryUser) (DirectoryUser, error)
	ListTeams(context.Context, string, int, int) ([]DirectoryTeam, int, error)
	PutTeam(context.Context, DirectoryTeam) (DirectoryTeam, error)
	PutMembership(context.Context, TeamMembership) (TeamMembership, error)
}

func (m AuthModule) directoryStore() (identityDirectoryStore, error) {
	if m.initErr != nil {
		return nil, m.initErr
	}
	store, ok := m.store.(identityDirectoryStore)
	if !ok || store == nil {
		return nil, errors.New("persistent identity directory is unavailable")
	}
	return store, nil
}

func validDirectoryID(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("._@:-", r) {
			return false
		}
	}
	return true
}

func validDirectoryStatus(value string) bool { return value == "active" || value == "disabled" }

func (s *PostgresVirtualKeyStore) ListUsers(ctx context.Context, teamID string, offset, limit int) ([]DirectoryUser, int, error) {
	rows, err := s.pool.Query(ctx, `SELECT u.id,COALESCE(u.email,''),u.name,u.status,u.roles,
		COALESCE(array_agg(m.team_id ORDER BY m.team_id) FILTER (WHERE m.team_id IS NOT NULL),'{}'),u.created_at,u.updated_at
		FROM users u LEFT JOIN auth_team_memberships m ON m.user_id=u.id
		WHERE ($1='' OR m.team_id=$1) GROUP BY u.id,u.email,u.name,u.status,u.roles,u.created_at,u.updated_at
		ORDER BY u.created_at DESC,u.id OFFSET $2 LIMIT $3`, teamID, offset, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("list directory users: %w", err)
	}
	defer rows.Close()
	result := make([]DirectoryUser, 0)
	for rows.Next() {
		var user DirectoryUser
		if err := rows.Scan(&user.ID, &user.Email, &user.Name, &user.Status, &user.Roles, &user.TeamIDs, &user.CreatedAt, &user.UpdatedAt); err != nil {
			return nil, 0, err
		}
		result = append(result, user)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(DISTINCT u.id) FROM users u LEFT JOIN auth_team_memberships m ON m.user_id=u.id WHERE ($1='' OR m.team_id=$1)`, teamID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count directory users: %w", err)
	}
	return result, total, nil
}

func (s *PostgresVirtualKeyStore) PutUser(ctx context.Context, user DirectoryUser) (DirectoryUser, error) {
	err := s.pool.QueryRow(ctx, `INSERT INTO users(id,email,name,status,roles) VALUES($1,NULLIF($2,''),$3,$4,$5)
		ON CONFLICT(id) DO UPDATE SET email=EXCLUDED.email,name=EXCLUDED.name,status=EXCLUDED.status,roles=EXCLUDED.roles,updated_at=now()
		RETURNING id,COALESCE(email,''),name,status,roles,created_at,updated_at`, user.ID, user.Email, user.Name, user.Status, nonNilStrings(user.Roles)).Scan(&user.ID, &user.Email, &user.Name, &user.Status, &user.Roles, &user.CreatedAt, &user.UpdatedAt)
	return user, err
}

func (s *PostgresVirtualKeyStore) ListTeams(ctx context.Context, teamID string, offset, limit int) ([]DirectoryTeam, int, error) {
	rows, err := s.pool.Query(ctx, `SELECT t.id,t.name,t.description,t.status,count(m.user_id),t.created_at,t.updated_at FROM auth_teams t
		LEFT JOIN auth_team_memberships m ON m.team_id=t.id WHERE ($1='' OR t.id=$1)
		GROUP BY t.id ORDER BY t.created_at DESC,t.id OFFSET $2 LIMIT $3`, teamID, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := make([]DirectoryTeam, 0)
	for rows.Next() {
		var team DirectoryTeam
		if err := rows.Scan(&team.ID, &team.Name, &team.Description, &team.Status, &team.MemberCount, &team.CreatedAt, &team.UpdatedAt); err != nil {
			return nil, 0, err
		}
		result = append(result, team)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM auth_teams WHERE ($1='' OR id=$1)`, teamID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count directory teams: %w", err)
	}
	return result, total, nil
}

func (s *PostgresVirtualKeyStore) PutTeam(ctx context.Context, team DirectoryTeam) (DirectoryTeam, error) {
	err := s.pool.QueryRow(ctx, `INSERT INTO auth_teams(id,name,description,status) VALUES($1,$2,$3,$4)
		ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name,description=EXCLUDED.description,status=EXCLUDED.status,updated_at=now()
		RETURNING id,name,description,status,created_at,updated_at`, team.ID, team.Name, team.Description, team.Status).Scan(&team.ID, &team.Name, &team.Description, &team.Status, &team.CreatedAt, &team.UpdatedAt)
	return team, err
}

func (s *PostgresVirtualKeyStore) PutMembership(ctx context.Context, membership TeamMembership) (TeamMembership, error) {
	err := s.pool.QueryRow(ctx, `INSERT INTO auth_team_memberships(team_id,user_id,roles) VALUES($1,$2,$3)
		ON CONFLICT(team_id,user_id) DO UPDATE SET roles=EXCLUDED.roles,updated_at=now()
		RETURNING team_id,user_id,roles,created_at,updated_at`, membership.TeamID, membership.UserID, nonNilStrings(membership.Roles)).Scan(&membership.TeamID, &membership.UserID, &membership.Roles, &membership.CreatedAt, &membership.UpdatedAt)
	return membership, err
}

func (s *PostgresVirtualKeyStore) ListMemberships(ctx context.Context, teamID string, limit int) ([]TeamMembership, error) {
	rows, err := s.pool.Query(ctx, `SELECT team_id,user_id,roles,created_at,updated_at
		FROM auth_team_memberships WHERE team_id=$1 ORDER BY created_at DESC,user_id LIMIT $2`, teamID, limit)
	if err != nil {
		return nil, fmt.Errorf("list team memberships: %w", err)
	}
	defer rows.Close()
	result := make([]TeamMembership, 0)
	for rows.Next() {
		var membership TeamMembership
		if err := rows.Scan(&membership.TeamID, &membership.UserID, &membership.Roles, &membership.CreatedAt, &membership.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, membership)
	}
	return result, rows.Err()
}

func (s *PostgresVirtualKeyStore) DeleteMembership(ctx context.Context, teamID, userID string) (bool, error) {
	result, err := s.pool.Exec(ctx, `DELETE FROM auth_team_memberships WHERE team_id=$1 AND user_id=$2`, teamID, userID)
	if err != nil {
		return false, fmt.Errorf("delete team membership: %w", err)
	}
	return result.RowsAffected() > 0, nil
}
