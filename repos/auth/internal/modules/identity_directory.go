package modules

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DirectoryUser struct {
	ID         string     `json:"id"`
	ExternalID string     `json:"external_id,omitempty"`
	Email      string     `json:"email,omitempty"`
	Name       string     `json:"name,omitempty"`
	Status     string     `json:"status"`
	Roles      []string   `json:"roles,omitempty"`
	TeamIDs    []string   `json:"team_ids,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
}

type DirectoryTeam struct {
	ID          string     `json:"id"`
	ExternalID  string     `json:"external_id,omitempty"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Status      string     `json:"status"`
	MemberCount int        `json:"member_count"`
	MemberIDs   []string   `json:"member_ids,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
}

type TeamMembership struct {
	TeamID    string    `json:"team_id"`
	UserID    string    `json:"user_id"`
	Roles     []string  `json:"roles,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

var (
	ErrInvalidDirectoryEntry = errors.New("invalid identity directory entry")
	ErrDirectoryConflict     = errors.New("identity directory entry already exists")
	ErrDirectoryNotFound     = errors.New("identity directory entry not found")
)

func (m AuthModule) ListDirectoryUsers(ctx context.Context, teamID string, offset, limit int, includeDeleted bool) ([]DirectoryUser, int, error) {
	store, err := m.directoryStore()
	if err != nil {
		return nil, 0, err
	}
	if offset < 0 || limit < 1 || limit > 500 || len(teamID) > 256 {
		return nil, 0, ErrInvalidDirectoryEntry
	}
	return store.ListUsers(ctx, strings.TrimSpace(teamID), offset, limit, includeDeleted)
}

func (m AuthModule) PutDirectoryUser(ctx context.Context, user DirectoryUser) (DirectoryUser, error) {
	store, err := m.directoryStore()
	if err != nil {
		return DirectoryUser{}, err
	}
	user.ID, user.ExternalID, user.Email, user.Name = strings.TrimSpace(user.ID), strings.TrimSpace(user.ExternalID), strings.TrimSpace(user.Email), strings.TrimSpace(user.Name)
	if !validDirectoryID(user.ID) || len(user.ExternalID) > 256 || len(user.Email) > 320 || len(user.Name) > 256 || !validDirectoryStatus(user.Status) || !validPolicyStrings(user.Roles) {
		return DirectoryUser{}, ErrInvalidDirectoryEntry
	}
	saved, err := store.PutUser(ctx, user)
	if isUniqueViolation(err) {
		return DirectoryUser{}, ErrDirectoryConflict
	}
	return saved, err
}

func (m AuthModule) CreateDirectoryUser(ctx context.Context, user DirectoryUser) (DirectoryUser, error) {
	store, err := m.directoryStore()
	if err != nil {
		return DirectoryUser{}, err
	}
	user.ID, user.ExternalID, user.Email, user.Name = strings.TrimSpace(user.ID), strings.TrimSpace(user.ExternalID), strings.TrimSpace(user.Email), strings.TrimSpace(user.Name)
	if !validDirectoryID(user.ID) || len(user.ExternalID) > 256 || len(user.Email) > 320 || len(user.Name) > 256 || !validDirectoryStatus(user.Status) || !validPolicyStrings(user.Roles) {
		return DirectoryUser{}, ErrInvalidDirectoryEntry
	}
	return store.CreateUser(ctx, user)
}

func (m AuthModule) GetDirectoryUser(ctx context.Context, id string) (DirectoryUser, error) {
	store, err := m.directoryStore()
	if err != nil {
		return DirectoryUser{}, err
	}
	id = strings.TrimSpace(id)
	if !validDirectoryID(id) {
		return DirectoryUser{}, ErrInvalidDirectoryEntry
	}
	return store.GetUser(ctx, id)
}

func (m AuthModule) FindDirectoryUser(ctx context.Context, attribute, value string) (DirectoryUser, bool, error) {
	store, err := m.directoryStore()
	if err != nil {
		return DirectoryUser{}, false, err
	}
	attribute, value = strings.TrimSpace(attribute), strings.TrimSpace(value)
	if (attribute != "userName" && attribute != "externalId") || value == "" || len(value) > 320 {
		return DirectoryUser{}, false, ErrInvalidDirectoryEntry
	}
	return store.FindUser(ctx, attribute, value)
}

func (m AuthModule) DeleteDirectoryUser(ctx context.Context, id string) (DirectoryUser, error) {
	store, err := m.directoryStore()
	if err != nil {
		return DirectoryUser{}, err
	}
	id = strings.TrimSpace(id)
	if !validDirectoryID(id) {
		return DirectoryUser{}, ErrInvalidDirectoryEntry
	}
	return store.DeleteUser(ctx, id)
}

func (m AuthModule) ListDirectoryTeams(ctx context.Context, teamID string, offset, limit int, includeDeleted bool) ([]DirectoryTeam, int, error) {
	store, err := m.directoryStore()
	if err != nil {
		return nil, 0, err
	}
	if offset < 0 || limit < 1 || limit > 500 || len(teamID) > 256 {
		return nil, 0, ErrInvalidDirectoryEntry
	}
	return store.ListTeams(ctx, strings.TrimSpace(teamID), offset, limit, includeDeleted)
}

func (m AuthModule) PutDirectoryTeam(ctx context.Context, team DirectoryTeam) (DirectoryTeam, error) {
	store, err := m.directoryStore()
	if err != nil {
		return DirectoryTeam{}, err
	}
	team.ID, team.ExternalID, team.Name, team.Description = strings.TrimSpace(team.ID), strings.TrimSpace(team.ExternalID), strings.TrimSpace(team.Name), strings.TrimSpace(team.Description)
	if !validDirectoryID(team.ID) || len(team.ExternalID) > 256 || team.Name == "" || len(team.Name) > 256 || len(team.Description) > 1024 || !validDirectoryStatus(team.Status) {
		return DirectoryTeam{}, ErrInvalidDirectoryEntry
	}
	saved, err := store.PutTeam(ctx, team)
	if isUniqueViolation(err) {
		return DirectoryTeam{}, ErrDirectoryConflict
	}
	return saved, err
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
	ListUsers(context.Context, string, int, int, bool) ([]DirectoryUser, int, error)
	GetUser(context.Context, string) (DirectoryUser, error)
	FindUser(context.Context, string, string) (DirectoryUser, bool, error)
	DeleteUser(context.Context, string) (DirectoryUser, error)
	CreateUser(context.Context, DirectoryUser) (DirectoryUser, error)
	PutUser(context.Context, DirectoryUser) (DirectoryUser, error)
	ListTeams(context.Context, string, int, int, bool) ([]DirectoryTeam, int, error)
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

func (s *PostgresVirtualKeyStore) ListUsers(ctx context.Context, teamID string, offset, limit int, includeDeleted bool) ([]DirectoryUser, int, error) {
	rows, err := s.pool.Query(ctx, `SELECT u.id,u.external_id,COALESCE(u.email,''),u.name,u.status,u.roles,
		COALESCE(array_agg(m.team_id ORDER BY m.team_id) FILTER (WHERE m.team_id IS NOT NULL),'{}'),u.created_at,u.updated_at,u.scim_deleted_at
		FROM users u LEFT JOIN auth_team_memberships m ON m.user_id=u.id
		WHERE ($1='' OR m.team_id=$1) AND ($4 OR u.scim_deleted_at IS NULL) GROUP BY u.id,u.external_id,u.email,u.name,u.status,u.roles,u.created_at,u.updated_at,u.scim_deleted_at
		ORDER BY u.created_at DESC,u.id OFFSET $2 LIMIT $3`, teamID, offset, limit, includeDeleted)
	if err != nil {
		return nil, 0, fmt.Errorf("list directory users: %w", err)
	}
	defer rows.Close()
	result := make([]DirectoryUser, 0)
	for rows.Next() {
		var user DirectoryUser
		if err := rows.Scan(&user.ID, &user.ExternalID, &user.Email, &user.Name, &user.Status, &user.Roles, &user.TeamIDs, &user.CreatedAt, &user.UpdatedAt, &user.DeletedAt); err != nil {
			return nil, 0, err
		}
		result = append(result, user)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(DISTINCT u.id) FROM users u LEFT JOIN auth_team_memberships m ON m.user_id=u.id WHERE ($1='' OR m.team_id=$1) AND ($2 OR u.scim_deleted_at IS NULL)`, teamID, includeDeleted).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count directory users: %w", err)
	}
	return result, total, nil
}

func (s *PostgresVirtualKeyStore) GetUser(ctx context.Context, id string) (DirectoryUser, error) {
	var user DirectoryUser
	err := s.pool.QueryRow(ctx, `SELECT id,external_id,COALESCE(email,''),name,status,roles,created_at,updated_at,scim_deleted_at FROM users WHERE id=$1`, id).
		Scan(&user.ID, &user.ExternalID, &user.Email, &user.Name, &user.Status, &user.Roles, &user.CreatedAt, &user.UpdatedAt, &user.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return DirectoryUser{}, ErrDirectoryNotFound
	}
	return user, err
}

func (s *PostgresVirtualKeyStore) FindUser(ctx context.Context, attribute, value string) (DirectoryUser, bool, error) {
	query := `SELECT id,external_id,COALESCE(email,''),name,status,roles,created_at,updated_at,scim_deleted_at FROM users WHERE external_id=$1 AND scim_deleted_at IS NULL`
	switch attribute {
	case "userName":
		query = `SELECT id,external_id,COALESCE(email,''),name,status,roles,created_at,updated_at,scim_deleted_at FROM users WHERE lower(email)=$1 AND scim_deleted_at IS NULL`
		value = strings.ToLower(value)
	case "externalId":
	default:
		return DirectoryUser{}, false, ErrInvalidDirectoryEntry
	}
	var user DirectoryUser
	err := s.pool.QueryRow(ctx, query, value).
		Scan(&user.ID, &user.ExternalID, &user.Email, &user.Name, &user.Status, &user.Roles, &user.CreatedAt, &user.UpdatedAt, &user.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return DirectoryUser{}, false, nil
	}
	return user, err == nil, err
}

func (s *PostgresVirtualKeyStore) DeleteUser(ctx context.Context, id string) (DirectoryUser, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DirectoryUser{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var user DirectoryUser
	err = tx.QueryRow(ctx, `UPDATE users SET status='disabled',scim_deleted_at=now(),updated_at=now() WHERE id=$1 AND scim_deleted_at IS NULL
		RETURNING id,external_id,COALESCE(email,''),name,status,roles,created_at,updated_at,scim_deleted_at`, id).
		Scan(&user.ID, &user.ExternalID, &user.Email, &user.Name, &user.Status, &user.Roles, &user.CreatedAt, &user.UpdatedAt, &user.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return DirectoryUser{}, ErrDirectoryNotFound
	}
	if err != nil {
		return DirectoryUser{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM auth_team_memberships WHERE user_id=$1`, id); err != nil {
		return DirectoryUser{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DirectoryUser{}, err
	}
	return user, nil
}

func (s *PostgresVirtualKeyStore) CreateUser(ctx context.Context, user DirectoryUser) (DirectoryUser, error) {
	err := s.pool.QueryRow(ctx, `INSERT INTO users(id,external_id,email,name,status,roles,scim_deleted_at) VALUES($1,$2,NULLIF($3,''),$4,$5,$6,$7)
		ON CONFLICT DO NOTHING RETURNING id,external_id,COALESCE(email,''),name,status,roles,created_at,updated_at,scim_deleted_at`, user.ID, user.ExternalID, user.Email, user.Name, user.Status, nonNilStrings(user.Roles), user.DeletedAt).
		Scan(&user.ID, &user.ExternalID, &user.Email, &user.Name, &user.Status, &user.Roles, &user.CreatedAt, &user.UpdatedAt, &user.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return DirectoryUser{}, ErrDirectoryConflict
	}
	return user, err
}

func isUniqueViolation(err error) bool {
	var postgresErr *pgconn.PgError
	return errors.As(err, &postgresErr) && postgresErr.Code == "23505"
}

func (s *PostgresVirtualKeyStore) PutUser(ctx context.Context, user DirectoryUser) (DirectoryUser, error) {
	err := s.pool.QueryRow(ctx, `INSERT INTO users(id,external_id,email,name,status,roles,scim_deleted_at) VALUES($1,$2,NULLIF($3,''),$4,$5,$6,$7)
		ON CONFLICT(id) DO UPDATE SET external_id=EXCLUDED.external_id,email=EXCLUDED.email,name=EXCLUDED.name,status=EXCLUDED.status,roles=EXCLUDED.roles,scim_deleted_at=EXCLUDED.scim_deleted_at,updated_at=now()
		RETURNING id,external_id,COALESCE(email,''),name,status,roles,created_at,updated_at,scim_deleted_at`, user.ID, user.ExternalID, user.Email, user.Name, user.Status, nonNilStrings(user.Roles), user.DeletedAt).Scan(&user.ID, &user.ExternalID, &user.Email, &user.Name, &user.Status, &user.Roles, &user.CreatedAt, &user.UpdatedAt, &user.DeletedAt)
	return user, err
}

func (s *PostgresVirtualKeyStore) ListTeams(ctx context.Context, teamID string, offset, limit int, includeDeleted bool) ([]DirectoryTeam, int, error) {
	rows, err := s.pool.Query(ctx, `SELECT t.id,t.external_id,t.name,t.description,t.status,
		COALESCE(array_agg(m.user_id ORDER BY m.user_id) FILTER (WHERE m.user_id IS NOT NULL),'{}'),t.created_at,t.updated_at,t.scim_deleted_at FROM auth_teams t
		LEFT JOIN auth_team_memberships m ON m.team_id=t.id WHERE ($1='' OR t.id=$1) AND ($4 OR t.scim_deleted_at IS NULL)
		GROUP BY t.id ORDER BY t.created_at DESC,t.id OFFSET $2 LIMIT $3`, teamID, offset, limit, includeDeleted)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := make([]DirectoryTeam, 0)
	for rows.Next() {
		var team DirectoryTeam
		if err := rows.Scan(&team.ID, &team.ExternalID, &team.Name, &team.Description, &team.Status, &team.MemberIDs, &team.CreatedAt, &team.UpdatedAt, &team.DeletedAt); err != nil {
			return nil, 0, err
		}
		team.MemberCount = len(team.MemberIDs)
		result = append(result, team)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM auth_teams WHERE ($1='' OR id=$1) AND ($2 OR scim_deleted_at IS NULL)`, teamID, includeDeleted).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count directory teams: %w", err)
	}
	return result, total, nil
}

func (s *PostgresVirtualKeyStore) PutTeam(ctx context.Context, team DirectoryTeam) (DirectoryTeam, error) {
	err := s.pool.QueryRow(ctx, `INSERT INTO auth_teams(id,external_id,name,description,status,scim_deleted_at) VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT(id) DO UPDATE SET external_id=EXCLUDED.external_id,name=EXCLUDED.name,description=EXCLUDED.description,status=EXCLUDED.status,scim_deleted_at=EXCLUDED.scim_deleted_at,updated_at=now()
		RETURNING id,external_id,name,description,status,created_at,updated_at,scim_deleted_at`, team.ID, team.ExternalID, team.Name, team.Description, team.Status, team.DeletedAt).Scan(&team.ID, &team.ExternalID, &team.Name, &team.Description, &team.Status, &team.CreatedAt, &team.UpdatedAt, &team.DeletedAt)
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
