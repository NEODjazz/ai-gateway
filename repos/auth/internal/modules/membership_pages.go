package modules

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// DirectoryMembership includes bounded display metadata from the same snapshot
// as the membership. Roles remain membership roles, never global approvals.
type DirectoryMembership struct {
	TeamMembership
	UserName   string `json:"user_name"`
	UserEmail  string `json:"user_email"`
	UserStatus string `json:"user_status"`
	TeamName   string `json:"team_name"`
	TeamStatus string `json:"team_status"`
}

type MembershipQuery struct {
	TeamID    string
	UserID    string
	Search    string
	SortBy    string
	SortOrder string
	Offset    int
	Limit     int
}

func (q MembershipQuery) valid() bool {
	return (q.TeamID != "" || q.UserID != "") && (q.TeamID == "" || validDirectoryID(q.TeamID)) && (q.UserID == "" || validDirectoryID(q.UserID)) && len(q.Search) <= 256 && !strings.ContainsRune(q.Search, 0) && q.Offset >= 0 && q.Limit >= 1 && q.Limit <= 500 && (q.SortBy == "" || oneOf(q.SortBy, "user", "team", "joined")) && (q.SortOrder == "" || q.SortOrder == "asc" || q.SortOrder == "desc")
}

type membershipPageStore interface {
	ListMembershipsPage(context.Context, MembershipQuery) ([]DirectoryMembership, int, error)
	CreateMembership(context.Context, TeamMembership) (TeamMembership, error)
}

func (m AuthModule) GetDirectoryTeamMetadata(ctx context.Context, id string) (DirectoryTeam, error) {
	if !validDirectoryID(id) {
		return DirectoryTeam{}, ErrInvalidDirectoryEntry
	}
	if m.initErr != nil {
		return DirectoryTeam{}, m.initErr
	}
	store, ok := m.store.(interface {
		GetTeamMetadata(context.Context, string) (DirectoryTeam, error)
	})
	if !ok {
		return DirectoryTeam{}, errors.New("team metadata is unavailable")
	}
	return store.GetTeamMetadata(ctx, id)
}

func (s *PostgresVirtualKeyStore) GetTeamMetadata(ctx context.Context, id string) (DirectoryTeam, error) {
	var team DirectoryTeam
	err := s.pool.QueryRow(ctx, `SELECT t.id,t.external_id,t.name,t.description,t.status,count(u.id),t.created_at,t.updated_at,t.scim_deleted_at
	 FROM auth_teams t LEFT JOIN auth_team_memberships m ON m.team_id=t.id LEFT JOIN users u ON u.id=m.user_id AND u.scim_deleted_at IS NULL
	 WHERE t.id=$1 GROUP BY t.id`, id).Scan(&team.ID, &team.ExternalID, &team.Name, &team.Description, &team.Status, &team.MemberCount, &team.CreatedAt, &team.UpdatedAt, &team.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return DirectoryTeam{}, ErrDirectoryNotFound
	}
	return team, err
}

func (m AuthModule) ListDirectoryMemberships(ctx context.Context, q MembershipQuery) ([]DirectoryMembership, int, error) {
	if !q.valid() {
		return nil, 0, ErrInvalidDirectoryEntry
	}
	if m.initErr != nil {
		return nil, 0, m.initErr
	}
	store, ok := m.store.(membershipPageStore)
	if !ok {
		return nil, 0, errors.New("persistent membership pages are unavailable")
	}
	return store.ListMembershipsPage(ctx, q)
}

func (m AuthModule) CreateTeamMembership(ctx context.Context, membership TeamMembership) (TeamMembership, error) {
	membership.TeamID, membership.UserID = strings.TrimSpace(membership.TeamID), strings.TrimSpace(membership.UserID)
	if !validDirectoryID(membership.TeamID) || !validDirectoryID(membership.UserID) || !validPolicyStrings(membership.Roles) {
		return TeamMembership{}, ErrInvalidDirectoryEntry
	}
	if m.initErr != nil {
		return TeamMembership{}, m.initErr
	}
	store, ok := m.store.(membershipPageStore)
	if !ok {
		return TeamMembership{}, errors.New("persistent membership creation is unavailable")
	}
	return store.CreateMembership(ctx, membership)
}

func (s *PostgresVirtualKeyStore) CreateMembership(ctx context.Context, member TeamMembership) (TeamMembership, error) {
	err := s.pool.QueryRow(ctx, `INSERT INTO auth_team_memberships(team_id,user_id,roles)
	 SELECT t.id,u.id,$3 FROM auth_teams t CROSS JOIN users u WHERE t.id=$1 AND u.id=$2 AND t.status='active' AND u.status='active' AND t.scim_deleted_at IS NULL AND u.scim_deleted_at IS NULL
	 ON CONFLICT DO NOTHING RETURNING team_id,user_id,roles,created_at,updated_at`, member.TeamID, member.UserID, nonNilStrings(member.Roles)).Scan(&member.TeamID, &member.UserID, &member.Roles, &member.CreatedAt, &member.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_team_memberships WHERE team_id=$1 AND user_id=$2)`, member.TeamID, member.UserID).Scan(&exists); err != nil {
			return TeamMembership{}, err
		}
		if exists {
			return TeamMembership{}, ErrDirectoryConflict
		}
		return TeamMembership{}, ErrInvalidDirectoryEntry
	}
	return member, err
}

func (s *PostgresVirtualKeyStore) ListMembershipsPage(ctx context.Context, q MembershipQuery) ([]DirectoryMembership, int, error) {
	if !q.valid() {
		return nil, 0, ErrInvalidDirectoryEntry
	}
	direction := "DESC"
	if q.SortOrder == "asc" {
		direction = "ASC"
	}
	column := "m.created_at"
	if q.SortBy == "user" {
		column = "lower(COALESCE(NULLIF(u.name,''),NULLIF(u.email,''),u.id))"
	}
	if q.SortBy == "team" {
		column = "lower(t.name)"
	}
	order := column + " " + direction + ",m.team_id " + direction + ",m.user_id " + direction
	const from = ` FROM auth_team_memberships m JOIN users u ON u.id=m.user_id JOIN auth_teams t ON t.id=m.team_id
	 WHERE ($1='' OR m.team_id=$1) AND ($2='' OR m.user_id=$2) AND u.scim_deleted_at IS NULL AND t.scim_deleted_at IS NULL
	 AND ($3='' OR strpos(lower(u.id || ' ' || COALESCE(u.email,'') || ' ' || u.name || ' ' || t.id || ' ' || t.name),lower($3))>0)`
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT m.team_id,m.user_id,m.roles,m.created_at,m.updated_at,u.name,COALESCE(u.email,''),u.status,t.name,t.status`+from+fmt.Sprintf(" ORDER BY %s OFFSET $4 LIMIT $5", order), q.TeamID, q.UserID, q.Search, q.Offset, q.Limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := make([]DirectoryMembership, 0)
	for rows.Next() {
		var member DirectoryMembership
		if err := rows.Scan(&member.TeamID, &member.UserID, &member.Roles, &member.CreatedAt, &member.UpdatedAt, &member.UserName, &member.UserEmail, &member.UserStatus, &member.TeamName, &member.TeamStatus); err != nil {
			return nil, 0, err
		}
		result = append(result, member)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*)`+from, q.TeamID, q.UserID, q.Search).Scan(&total); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return result, total, nil
}
