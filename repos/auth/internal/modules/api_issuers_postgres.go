package modules

import "context"

func (s *PostgresVirtualKeyStore) ListAPIIssuers(ctx context.Context) ([]apiIssuerRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,name,COALESCE(organization_id,''),issuer,audience,revision,payload FROM auth_api_issuers ORDER BY id LIMIT 17`)
	if err != nil {
		return nil, ErrSSOUnavailable
	}
	defer rows.Close()
	result := []apiIssuerRow{}
	for rows.Next() {
		var row apiIssuerRow
		if err = rows.Scan(&row.ID, &row.Name, &row.OrganizationID, &row.Issuer, &row.Audience, &row.Revision, &row.Payload); err != nil {
			return nil, ErrSSOUnavailable
		}
		result = append(result, row)
	}
	if rows.Err() != nil || len(result) > maxAPIIssuers {
		return nil, ErrSSOUnavailable
	}
	return result, nil
}
func (s *PostgresVirtualKeyStore) CreateAPIIssuer(ctx context.Context, issuer APIIssuer, payload []byte) error {
	if !validAPIIssuer(issuer) || len(payload) > 65536 || len(payload) == 0 {
		return ErrSSOConfiguration
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ErrSSOUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(731503154)`); err != nil {
		return ErrSSOUnavailable
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM auth_api_issuers`).Scan(&count); err != nil {
		return ErrSSOUnavailable
	}
	if count >= maxAPIIssuers {
		return ErrSSOConfiguration
	}
	if issuer.OrganizationID != "" {
		var active bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_organizations WHERE id=$1 AND status='active')`, issuer.OrganizationID).Scan(&active); err != nil {
			return ErrSSOUnavailable
		}
		if !active {
			return ErrSSOConfiguration
		}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO auth_api_issuers(id,name,organization_id,issuer,audience,payload) VALUES($1,$2,NULLIF($3,''),$4,$5,$6) ON CONFLICT DO NOTHING`, issuer.ID, issuer.Name, issuer.OrganizationID, issuer.Issuer, issuer.Audience, payload)
	if err != nil {
		return ErrSSOUnavailable
	}
	if tag.RowsAffected() != 1 {
		return ErrSSOConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return ErrSSOUnavailable
	}
	return nil
}
func (s *PostgresVirtualKeyStore) SaveAPIIssuer(ctx context.Context, id string, expected int64, payload []byte) error {
	tag, err := s.pool.Exec(ctx, `UPDATE auth_api_issuers SET payload=$1,revision=revision+1,updated_at=now() WHERE id=$2 AND revision=$3`, payload, id, expected)
	if err != nil {
		return ErrSSOUnavailable
	}
	if tag.RowsAffected() != 1 {
		return ErrSSOConflict
	}
	return nil
}
