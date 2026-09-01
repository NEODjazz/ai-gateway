package modules

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type StoredVirtualKey struct {
	ID             string
	Alias          string
	Description    string
	Tags           []string
	UserID         string
	TeamID         string
	OrganizationID string
	Roles          []string
	AccessGroupIDs []string
	AllowedModels  []string
	AllowedTools   []string
	RateLimitRPM   int
	RateLimitTPM   int
	RotationFamily string
	RotatedFromID  string
	ExpiresAt      *time.Time
}

type VirtualKeyStore interface {
	Lookup(ctx context.Context, tokenHash string) (StoredVirtualKey, bool, error)
	Ready(ctx context.Context) error
	Close()
}

type PostgresVirtualKeyStore struct{ pool *pgxpool.Pool }

func NewPostgresVirtualKeyStore(dsn string) (*PostgresVirtualKeyStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("auth postgres dsn is required")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, errors.New("invalid auth postgres configuration")
	}
	return &PostgresVirtualKeyStore{pool: pool}, nil
}

func (s *PostgresVirtualKeyStore) Lookup(ctx context.Context, tokenHash string) (StoredVirtualKey, bool, error) {
	if s == nil || s.pool == nil {
		return StoredVirtualKey{}, false, errors.New("auth key store is not initialized")
	}
	var key StoredVirtualKey
	err := s.pool.QueryRow(ctx, `
		UPDATE auth_virtual_keys
		SET last_used_at = now()
		WHERE token_hash = $1
		  AND revoked_at IS NULL
		  AND disabled_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id=auth_virtual_keys.user_id AND u.status<>'active')
		  AND NOT EXISTS (SELECT 1 FROM auth_teams t WHERE t.id=auth_virtual_keys.team_id AND t.status<>'active')
		  AND NOT EXISTS (SELECT 1 FROM auth_organizations o WHERE o.id=auth_virtual_keys.organization_id AND o.status<>'active')
		  AND (expires_at IS NULL OR expires_at > now())
		RETURNING id, alias, tags, COALESCE(user_id, ''), COALESCE(team_id, ''), COALESCE(organization_id, ''), roles, access_group_ids, allowed_models, allowed_tools,
		          rate_limit_rpm, rate_limit_tpm, rotation_family_id,
		          COALESCE(rotated_from_id, ''), expires_at`, tokenHash).Scan(
		&key.ID, &key.Alias, &key.Tags, &key.UserID, &key.TeamID, &key.OrganizationID, &key.Roles, &key.AccessGroupIDs, &key.AllowedModels, &key.AllowedTools,
		&key.RateLimitRPM, &key.RateLimitTPM, &key.RotationFamily,
		&key.RotatedFromID, &key.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredVirtualKey{}, false, nil
	}
	if err != nil {
		return StoredVirtualKey{}, false, fmt.Errorf("lookup virtual key: %w", err)
	}
	return key, true, nil
}

func (s *PostgresVirtualKeyStore) Ready(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return errors.New("auth key store is not initialized")
	}
	if err := s.pool.Ping(ctx); err != nil {
		return errors.New("auth postgres is unavailable")
	}
	var migrationExists, toolsColumnExists, metadataColumnExists, directoryTableExists, ownershipColumnExists, accessGroupsColumnExists bool
	if err := s.pool.QueryRow(ctx, `
		SELECT to_regclass('public.auth_virtual_keys') IS NOT NULL,
		       EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='auth_virtual_keys' AND column_name='allowed_tools'),
		       EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='auth_virtual_keys' AND column_name='disabled_at'),
		       to_regclass('public.auth_team_memberships') IS NOT NULL,
		       EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='auth_virtual_keys' AND column_name='organization_id'),
		       EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='auth_virtual_keys' AND column_name='access_group_ids')`).Scan(&migrationExists, &toolsColumnExists, &metadataColumnExists, &directoryTableExists, &ownershipColumnExists, &accessGroupsColumnExists); err != nil || !migrationExists || !toolsColumnExists || !metadataColumnExists || !directoryTableExists || !ownershipColumnExists || !accessGroupsColumnExists {
		return errors.New("auth virtual-key migration is not applied")
	}
	return nil
}

func (s *PostgresVirtualKeyStore) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

func (s *PostgresVirtualKeyStore) Create(ctx context.Context, key StoredVirtualKey, tokenHash string) error {
	if key.ID == "" || (key.UserID == "" && key.TeamID == "" && key.OrganizationID == "") || tokenHash == "" {
		return errors.New("virtual key id, owner, and token hash are required")
	}
	family := key.RotationFamily
	if family == "" {
		family = key.ID
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO auth_virtual_keys
		(id, token_hash, alias, description, tags, user_id, team_id, organization_id, roles, access_group_ids, allowed_models, allowed_tools, rate_limit_rpm,
		 rate_limit_tpm, rotation_family_id, rotated_from_id, expires_at)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),$9,$10,$11,$12,$13,$14,$15,NULLIF($16,''),$17)`,
		key.ID, tokenHash, key.Alias, key.Description, nonNilStrings(key.Tags), key.UserID, key.TeamID, key.OrganizationID, nonNilStrings(key.Roles), nonNilStrings(key.AccessGroupIDs),
		nonNilStrings(key.AllowedModels), nonNilStrings(key.AllowedTools), key.RateLimitRPM, key.RateLimitTPM, family, key.RotatedFromID, key.ExpiresAt)
	return err
}

func (s *PostgresVirtualKeyStore) List(ctx context.Context, limit int) ([]VirtualKeyMetadata, error) {
	page, err := s.ListPage(ctx, VirtualKeyListQuery{Limit: limit, SortBy: "created", SortOrder: "desc"})
	return page.Data, err
}

func (s *PostgresVirtualKeyStore) ListPage(ctx context.Context, query VirtualKeyListQuery) (VirtualKeyPage, error) {
	if s == nil || s.pool == nil {
		return VirtualKeyPage{}, errors.New("auth key store is not initialized")
	}
	orderBy := map[string]string{
		"key": "id", "alias": "alias", "organization": "organization_id", "team": "team_id",
		"user": "user_id", "created": "created_at", "status": "status_rank",
	}[query.SortBy]
	if orderBy == "" {
		return VirtualKeyPage{}, errors.New("invalid virtual key sort field")
	}
	order := "DESC"
	if query.SortOrder == "asc" {
		order = "ASC"
	}
	const filteredKeys = `
		FROM auth_virtual_keys k
		WHERE ($1='' OR k.alias ILIKE '%' || $1 || '%')
		  AND ($2='' OR k.organization_id=$2
		       OR EXISTS (SELECT 1 FROM auth_organization_teams ot WHERE ot.organization_id=$2 AND ot.team_id=k.team_id)
		       OR EXISTS (SELECT 1 FROM auth_team_memberships tm JOIN auth_organization_teams ot ON ot.team_id=tm.team_id WHERE ot.organization_id=$2 AND tm.user_id=k.user_id))
		  AND ($3='' OR k.team_id=$3 OR EXISTS (SELECT 1 FROM auth_team_memberships tm WHERE tm.team_id=$3 AND tm.user_id=k.user_id))
		  AND ($4='' OR k.user_id=$4)
		  AND ($5='' OR k.id ILIKE '%' || $5 || '%')
		  AND ($6='' OR k.access_group_ids @> ARRAY[$6]::TEXT[])
		  AND ($7='' OR CASE $7
		       WHEN 'active' THEN k.revoked_at IS NULL AND k.disabled_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now())
		       WHEN 'disabled' THEN k.revoked_at IS NULL AND k.disabled_at IS NOT NULL
		       WHEN 'revoked' THEN k.revoked_at IS NOT NULL
		       WHEN 'expired' THEN k.revoked_at IS NULL AND k.disabled_at IS NULL AND k.expires_at IS NOT NULL AND k.expires_at<=now()
		       WHEN 'non_revoked' THEN k.revoked_at IS NULL
		       ELSE false END)`
	args := []any{query.Search, query.OrganizationID, query.TeamID, query.UserID, query.KeyID, query.AccessGroupID, query.Status}
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) "+filteredKeys, args...).Scan(&total); err != nil {
		return VirtualKeyPage{}, fmt.Errorf("count virtual keys: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, alias, description, tags, COALESCE(user_id, ''), COALESCE(team_id, ''), COALESCE(organization_id, ''), roles, access_group_ids, allowed_models, allowed_tools,
		       rate_limit_rpm, rate_limit_tpm, rotation_family_id,
		       COALESCE(rotated_from_id, ''), COALESCE(rotated_to_id, ''),
		       expires_at, revoked_at, disabled_at, last_used_at, created_at,
		       CASE WHEN revoked_at IS NOT NULL THEN 3 WHEN disabled_at IS NOT NULL THEN 2 WHEN expires_at IS NOT NULL AND expires_at<=now() THEN 1 ELSE 0 END AS status_rank
		`+filteredKeys+" ORDER BY "+orderBy+" "+order+", id "+order+" LIMIT $8 OFFSET $9", append(args, query.Limit, query.Offset)...)
	if err != nil {
		return VirtualKeyPage{}, fmt.Errorf("list virtual keys: %w", err)
	}
	defer rows.Close()
	keys := make([]VirtualKeyMetadata, 0)
	for rows.Next() {
		var key VirtualKeyMetadata
		var statusRank int
		if err := rows.Scan(&key.ID, &key.Alias, &key.Description, &key.Tags, &key.UserID, &key.TeamID, &key.OrganizationID, &key.Roles, &key.AccessGroupIDs, &key.AllowedModels, &key.AllowedTools,
			&key.RateLimitRPM, &key.RateLimitTPM, &key.RotationFamily, &key.RotatedFromID, &key.RotatedToID,
			&key.ExpiresAt, &key.RevokedAt, &key.DisabledAt, &key.LastUsedAt, &key.CreatedAt, &statusRank); err != nil {
			return VirtualKeyPage{}, fmt.Errorf("scan virtual key metadata: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return VirtualKeyPage{}, fmt.Errorf("list virtual keys: %w", err)
	}
	return VirtualKeyPage{Data: keys, Total: total, Limit: query.Limit, Offset: query.Offset}, nil
}

func (s *PostgresVirtualKeyStore) Update(ctx context.Context, id string, key StoredVirtualKey) (bool, error) {
	result, err := s.pool.Exec(ctx, `UPDATE auth_virtual_keys SET alias=$2, description=$3, tags=$4, user_id=NULLIF($5,''),
		team_id=NULLIF($6,''), organization_id=NULLIF($7,''), roles=$8, access_group_ids=$9, allowed_models=$10, allowed_tools=$11, rate_limit_rpm=$12,
		rate_limit_tpm=$13, expires_at=$14 WHERE id=$1 AND revoked_at IS NULL`, id, key.Alias, key.Description,
		nonNilStrings(key.Tags), key.UserID, key.TeamID, key.OrganizationID, nonNilStrings(key.Roles), nonNilStrings(key.AccessGroupIDs),
		nonNilStrings(key.AllowedModels), nonNilStrings(key.AllowedTools), key.RateLimitRPM, key.RateLimitTPM, key.ExpiresAt)
	return result.RowsAffected() == 1, err
}

func (s *PostgresVirtualKeyStore) SetDisabled(ctx context.Context, id string, disabled bool) (bool, error) {
	if disabled {
		result, err := s.pool.Exec(ctx, `UPDATE auth_virtual_keys SET disabled_at=now() WHERE id=$1 AND revoked_at IS NULL AND disabled_at IS NULL`, id)
		return result.RowsAffected() == 1, err
	}
	result, err := s.pool.Exec(ctx, `UPDATE auth_virtual_keys SET disabled_at=NULL WHERE id=$1 AND revoked_at IS NULL AND disabled_at IS NOT NULL`, id)
	return result.RowsAffected() == 1, err
}

func (s *PostgresVirtualKeyStore) Revoke(ctx context.Context, id string) (bool, error) {
	result, err := s.pool.Exec(ctx, `UPDATE auth_virtual_keys SET revoked_at = COALESCE(revoked_at, now()) WHERE id = $1 AND revoked_at IS NULL`, id)
	return result.RowsAffected() == 1, err
}

func (s *PostgresVirtualKeyStore) Rotate(ctx context.Context, oldID string, replacement StoredVirtualKey, tokenHash string) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var family string
	if err := tx.QueryRow(ctx, `SELECT rotation_family_id FROM auth_virtual_keys WHERE id=$1 AND revoked_at IS NULL FOR UPDATE`, oldID).Scan(&family); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVirtualKeyNotFound
		}
		return err
	}
	if replacement.ID == "" || (replacement.UserID == "" && replacement.TeamID == "" && replacement.OrganizationID == "") || tokenHash == "" {
		return errors.New("replacement key id, owner, and token hash are required")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_virtual_keys
		(id, token_hash, alias, description, tags, user_id, team_id, organization_id, roles, access_group_ids, allowed_models, allowed_tools, rate_limit_rpm,
		 rate_limit_tpm, rotation_family_id, rotated_from_id, expires_at)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		replacement.ID, tokenHash, replacement.Alias, replacement.Description, nonNilStrings(replacement.Tags), replacement.UserID, replacement.TeamID, replacement.OrganizationID,
		nonNilStrings(replacement.Roles), nonNilStrings(replacement.AccessGroupIDs), nonNilStrings(replacement.AllowedModels), nonNilStrings(replacement.AllowedTools), replacement.RateLimitRPM,
		replacement.RateLimitTPM, family, oldID, replacement.ExpiresAt); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_virtual_keys SET revoked_at=now(), rotated_to_id=$2 WHERE id=$1`, oldID, replacement.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
