package controlstore

import (
	"context"
	"errors"

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
