package controlstore

import (
	"context"
	"errors"
	"time"

	"ai-gateway-gateway/internal/skillstate"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) ClaimSkill(ctx context.Context, ownership skillstate.Ownership) (skillstate.Ownership, error) {
	if s == nil || s.pool == nil {
		return skillstate.Ownership{}, skillstate.ErrUnavailable
	}
	if ownership.SkillID == "" || ownership.OwnerKey == "" || ownership.EndpointID == "" {
		return skillstate.Ownership{}, skillstate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `INSERT INTO gateway_skill_ownership (skill_id,owner_key,endpoint_id) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, ownership.SkillID, ownership.OwnerKey, ownership.EndpointID)
	if err != nil {
		return skillstate.Ownership{}, err
	}
	if command.RowsAffected() != 1 {
		return skillstate.Ownership{}, skillstate.ErrConflict
	}
	if err := s.pool.QueryRow(ctx, `SELECT created_at FROM gateway_skill_ownership WHERE skill_id=$1`, ownership.SkillID).Scan(&ownership.CreatedAt); err != nil {
		return skillstate.Ownership{}, err
	}
	return ownership, nil
}

func (s *PostgresStore) ResolveSkill(ctx context.Context, owner, skillID string) (skillstate.Ownership, error) {
	if s == nil || s.pool == nil {
		return skillstate.Ownership{}, skillstate.ErrUnavailable
	}
	if owner == "" || skillID == "" {
		return skillstate.Ownership{}, skillstate.ErrInvalid
	}
	ownership := skillstate.Ownership{OwnerKey: owner, SkillID: skillID}
	err := s.pool.QueryRow(ctx, `SELECT endpoint_id,created_at FROM gateway_skill_ownership WHERE owner_key=$1 AND skill_id=$2`, owner, skillID).Scan(&ownership.EndpointID, &ownership.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return skillstate.Ownership{}, skillstate.ErrNotFound
	}
	if err != nil {
		return skillstate.Ownership{}, err
	}
	return ownership, nil
}

func (s *PostgresStore) OwnedSkills(ctx context.Context, owner, endpoint string, skillIDs []string) (map[string]bool, error) {
	if s == nil || s.pool == nil {
		return nil, skillstate.ErrUnavailable
	}
	if owner == "" || endpoint == "" || len(skillIDs) > 1000 {
		return nil, skillstate.ErrInvalid
	}
	owned := make(map[string]bool)
	if len(skillIDs) == 0 {
		return owned, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT skill_id FROM gateway_skill_ownership WHERE owner_key=$1 AND endpoint_id=$2 AND skill_id=ANY($3)`, owner, endpoint, skillIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		owned[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return owned, nil
}

func (s *PostgresStore) DeleteSkill(ctx context.Context, owner, skillID string) error {
	if s == nil || s.pool == nil {
		return skillstate.ErrUnavailable
	}
	if owner == "" || skillID == "" {
		return skillstate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM gateway_skill_ownership WHERE owner_key=$1 AND skill_id=$2`, owner, skillID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return skillstate.ErrNotFound
	}
	return nil
}

func (s *PostgresStore) SaveSkillExecution(ctx context.Context, execution skillstate.Execution) error {
	if s == nil || s.pool == nil {
		return skillstate.ErrUnavailable
	}
	if execution.ContainerID == "" || execution.OwnerKey == "" || execution.EndpointID == "" || !execution.ExpiresAt.After(time.Now().UTC()) {
		return skillstate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `WITH expired AS (
		DELETE FROM gateway_skill_executions WHERE container_id IN (
			SELECT container_id FROM gateway_skill_executions WHERE expires_at<=now() ORDER BY expires_at LIMIT 1000
		)
	)
		INSERT INTO gateway_skill_executions (container_id,owner_key,endpoint_id,expires_at)
		VALUES ($1,$2,$3,$4) ON CONFLICT (container_id) DO UPDATE SET expires_at=EXCLUDED.expires_at,updated_at=now()
		WHERE gateway_skill_executions.owner_key=EXCLUDED.owner_key AND gateway_skill_executions.endpoint_id=EXCLUDED.endpoint_id`,
		execution.ContainerID, execution.OwnerKey, execution.EndpointID, execution.ExpiresAt)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return skillstate.ErrConflict
	}
	return nil
}

func (s *PostgresStore) ResolveSkillExecution(ctx context.Context, owner, containerID string) (skillstate.Execution, error) {
	if s == nil || s.pool == nil {
		return skillstate.Execution{}, skillstate.ErrUnavailable
	}
	if owner == "" || containerID == "" {
		return skillstate.Execution{}, skillstate.ErrInvalid
	}
	execution := skillstate.Execution{OwnerKey: owner, ContainerID: containerID}
	err := s.pool.QueryRow(ctx, `SELECT endpoint_id,expires_at,created_at,updated_at FROM gateway_skill_executions
		WHERE owner_key=$1 AND container_id=$2 AND expires_at>now()`, owner, containerID).
		Scan(&execution.EndpointID, &execution.ExpiresAt, &execution.CreatedAt, &execution.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return skillstate.Execution{}, skillstate.ErrNotFound
	}
	if err != nil {
		return skillstate.Execution{}, err
	}
	return execution, nil
}
