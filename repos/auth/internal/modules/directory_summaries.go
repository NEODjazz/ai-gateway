package modules

import (
	"context"
	"errors"
)

type DirectorySummaryQuery struct {
	ScopeType string   `json:"scope_type"`
	IDs       []string `json:"ids"`
}

func (q DirectorySummaryQuery) Valid() bool {
	if (q.ScopeType != "user" && q.ScopeType != "team") || len(q.IDs) == 0 || len(q.IDs) > 200 {
		return false
	}
	seen := make(map[string]bool, len(q.IDs))
	for _, id := range q.IDs {
		if !validDirectoryID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// DirectorySummary contains counts and bounded owner metadata, never key data.
type DirectorySummary struct {
	ID           string                `json:"id"`
	KeyCount     int                   `json:"key_count"`
	Organization *OrganizationMetadata `json:"organization"`
}

func (m AuthModule) DirectorySummaries(ctx context.Context, q DirectorySummaryQuery) ([]DirectorySummary, error) {
	if !q.Valid() {
		return nil, ErrInvalidDirectoryEntry
	}
	if m.initErr != nil {
		return nil, m.initErr
	}
	s, ok := m.store.(interface {
		DirectorySummaries(context.Context, DirectorySummaryQuery) ([]DirectorySummary, error)
	})
	if !ok {
		return nil, errors.New("directory summaries are unavailable")
	}
	return s.DirectorySummaries(ctx, q)
}

func (s *PostgresVirtualKeyStore) DirectorySummaries(ctx context.Context, q DirectorySummaryQuery) ([]DirectorySummary, error) {
	if !q.Valid() {
		return nil, ErrInvalidDirectoryEntry
	}
	// Match the scoped key page, including member-owned keys and all statuses.
	statement := `WITH selected AS (SELECT id FROM auth_teams WHERE id=ANY($1)), counts AS (
	 SELECT id,SUM(key_count)::bigint AS key_count FROM (
	  SELECT k.team_id AS id,count(*) AS key_count FROM auth_virtual_keys k JOIN selected t ON t.id=k.team_id GROUP BY k.team_id
	  UNION ALL
	  SELECT m.team_id AS id,count(*) AS key_count FROM auth_team_memberships m JOIN selected t ON t.id=m.team_id JOIN auth_virtual_keys k ON k.user_id=m.user_id WHERE k.team_id IS DISTINCT FROM m.team_id GROUP BY m.team_id
	 ) scoped_counts GROUP BY id
	) SELECT t.id,COALESCE(c.key_count,0),
	 COALESCE(o.id,''),COALESCE(o.name,''),COALESCE(o.description,''),COALESCE(o.status,''),COALESCE(o.created_at,'epoch'::timestamptz),COALESCE(o.updated_at,'epoch'::timestamptz)
	 FROM selected t LEFT JOIN counts c ON c.id=t.id LEFT JOIN auth_organization_teams ot ON ot.team_id=t.id LEFT JOIN auth_organizations o ON o.id=ot.organization_id ORDER BY t.id`
	if q.ScopeType == "user" {
		statement = `SELECT u.id,COALESCE(k.key_count,0),'','','','','epoch'::timestamptz,'epoch'::timestamptz FROM users u LEFT JOIN (SELECT user_id,count(*) AS key_count FROM auth_virtual_keys WHERE user_id=ANY($1) GROUP BY user_id) k ON k.user_id=u.id WHERE u.id=ANY($1) ORDER BY u.id`
	}
	rows, err := s.pool.Query(ctx, statement, q.IDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]DirectorySummary, 0, len(q.IDs))
	for rows.Next() {
		var summary DirectorySummary
		var org OrganizationMetadata
		if err := rows.Scan(&summary.ID, &summary.KeyCount, &org.ID, &org.Name, &org.Description, &org.Status, &org.CreatedAt, &org.UpdatedAt); err != nil {
			return nil, err
		}
		if org.ID != "" {
			summary.Organization = &org
		}
		result = append(result, summary)
	}
	return result, rows.Err()
}
