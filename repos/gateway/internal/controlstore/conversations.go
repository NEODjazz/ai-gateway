package controlstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"ai-gateway-gateway/internal/conversationstate"
	"github.com/jackc/pgx/v5"
)

type conversationRow interface{ Scan(...any) error }

func scanConversation(row conversationRow) (conversationstate.Conversation, error) {
	var value conversationstate.Conversation
	err := row.Scan(&value.ID, &value.OwnerKey, &value.Metadata, &value.Revision, &value.CreatedAt, &value.UpdatedAt)
	return value, err
}

func scanConversationItem(row conversationRow) (conversationstate.Item, error) {
	var value conversationstate.Item
	err := row.Scan(&value.ID, &value.ConversationID, &value.OwnerKey, &value.Payload, &value.Ordinal, &value.CreatedAt)
	return value, err
}

func validConversationMetadata(value []byte) bool {
	if len(value) < 2 || len(value) > conversationstate.MaxMetadataBytes {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}

func validConversationItem(value []byte) bool {
	if len(value) < 2 || len(value) > conversationstate.MaxItemBytes {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}

func validConversationRecord(record conversationstate.Conversation) bool {
	return record.ID != "" && len(record.ID) <= 128 && record.OwnerKey != "" && len(record.OwnerKey) <= 256 && validConversationMetadata(record.Metadata)
}

func validConversationItems(items []conversationstate.Item, owner, conversationID string) bool {
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if item.ID == "" || len(item.ID) > 128 || item.OwnerKey != owner || item.ConversationID != conversationID || !validConversationItem(item.Payload) || seen[item.ID] {
			return false
		}
		seen[item.ID] = true
	}
	return true
}

func (s *PostgresStore) CreateConversation(ctx context.Context, record conversationstate.Conversation, items []conversationstate.Item, ownerQuota, itemQuota int) (conversationstate.Conversation, []conversationstate.Item, error) {
	if s == nil || s.pool == nil {
		return conversationstate.Conversation{}, nil, conversationstate.ErrUnavailable
	}
	if !validConversationRecord(record) || ownerQuota < 1 || itemQuota < 1 || len(items) > itemQuota || !validConversationItems(items, record.OwnerKey, record.ID) {
		return conversationstate.Conversation{}, nil, conversationstate.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return conversationstate.Conversation{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 11))`, record.OwnerKey); err != nil {
		return conversationstate.Conversation{}, nil, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_conversations WHERE owner_key=$1`, record.OwnerKey).Scan(&count); err != nil {
		return conversationstate.Conversation{}, nil, err
	}
	if count >= ownerQuota {
		return conversationstate.Conversation{}, nil, conversationstate.ErrQuotaExceeded
	}
	created, err := scanConversation(tx.QueryRow(ctx, `INSERT INTO gateway_conversations (id,owner_key,metadata) VALUES ($1,$2,$3::jsonb) ON CONFLICT DO NOTHING RETURNING id,owner_key,metadata,revision,created_at,updated_at`, record.ID, record.OwnerKey, record.Metadata))
	if errors.Is(err, pgx.ErrNoRows) {
		return conversationstate.Conversation{}, nil, conversationstate.ErrConflict
	}
	if err != nil {
		return conversationstate.Conversation{}, nil, err
	}
	createdItems, err := insertConversationItems(ctx, tx, items)
	if err != nil {
		return conversationstate.Conversation{}, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return conversationstate.Conversation{}, nil, err
	}
	return created, createdItems, nil
}

func (s *PostgresStore) GetConversation(ctx context.Context, owner, id string) (conversationstate.Conversation, error) {
	if s == nil || s.pool == nil {
		return conversationstate.Conversation{}, conversationstate.ErrUnavailable
	}
	if owner == "" || id == "" {
		return conversationstate.Conversation{}, conversationstate.ErrInvalid
	}
	value, err := scanConversation(s.pool.QueryRow(ctx, `SELECT id,owner_key,metadata,revision,created_at,updated_at FROM gateway_conversations WHERE owner_key=$1 AND id=$2`, owner, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return conversationstate.Conversation{}, conversationstate.ErrNotFound
	}
	return value, err
}

func (s *PostgresStore) UpdateConversation(ctx context.Context, owner, id string, metadata []byte, revision int64) (conversationstate.Conversation, error) {
	if s == nil || s.pool == nil {
		return conversationstate.Conversation{}, conversationstate.ErrUnavailable
	}
	if owner == "" || id == "" || revision < 1 || !validConversationMetadata(metadata) {
		return conversationstate.Conversation{}, conversationstate.ErrInvalid
	}
	value, err := scanConversation(s.pool.QueryRow(ctx, `UPDATE gateway_conversations SET metadata=$3::jsonb,revision=revision+1,updated_at=now() WHERE owner_key=$1 AND id=$2 AND revision=$4 AND (active_execution_id IS NULL OR (durable_active=false AND lease_until<=now())) RETURNING id,owner_key,metadata,revision,created_at,updated_at`, owner, id, metadata, revision))
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := s.GetConversation(ctx, owner, id); errors.Is(getErr, conversationstate.ErrNotFound) {
			return conversationstate.Conversation{}, conversationstate.ErrNotFound
		}
		return conversationstate.Conversation{}, conversationstate.ErrConflict
	}
	return value, err
}

func (s *PostgresStore) DeleteConversation(ctx context.Context, owner, id string) error {
	if s == nil || s.pool == nil {
		return conversationstate.ErrUnavailable
	}
	if owner == "" || id == "" {
		return conversationstate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM gateway_conversations WHERE owner_key=$1 AND id=$2 AND (active_execution_id IS NULL OR (durable_active=false AND lease_until<=now()))`, owner, id)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 1 {
		return nil
	}
	if _, getErr := s.GetConversation(ctx, owner, id); errors.Is(getErr, conversationstate.ErrNotFound) {
		return conversationstate.ErrNotFound
	}
	return conversationstate.ErrConflict
}

func (s *PostgresStore) CreateItems(ctx context.Context, owner, conversationID string, items []conversationstate.Item, itemQuota int) ([]conversationstate.Item, error) {
	if s == nil || s.pool == nil {
		return nil, conversationstate.ErrUnavailable
	}
	if owner == "" || conversationID == "" || len(items) < 1 || len(items) > 20 || itemQuota < 1 || !validConversationItems(items, owner, conversationID) {
		return nil, conversationstate.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockConversation(ctx, tx, owner, conversationID); err != nil {
		return nil, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_conversation_items WHERE owner_key=$1 AND conversation_id=$2`, owner, conversationID).Scan(&count); err != nil {
		return nil, err
	}
	if count > itemQuota-len(items) {
		return nil, conversationstate.ErrQuotaExceeded
	}
	created, err := insertConversationItems(ctx, tx, items)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE gateway_conversations SET revision=revision+1,updated_at=now() WHERE owner_key=$1 AND id=$2`, owner, conversationID); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return created, nil
}

func (s *PostgresStore) ListItems(ctx context.Context, owner, conversationID string, options conversationstate.PageOptions) ([]conversationstate.Item, bool, error) {
	if s == nil || s.pool == nil {
		return nil, false, conversationstate.ErrUnavailable
	}
	if owner == "" || conversationID == "" || options.Limit < 1 || options.Limit > 100 || options.Order != "asc" && options.Order != "desc" {
		return nil, false, conversationstate.ErrInvalid
	}
	if _, err := s.GetConversation(ctx, owner, conversationID); err != nil {
		return nil, false, err
	}
	var cursor *int64
	if options.After != "" {
		var ordinal int64
		err := s.pool.QueryRow(ctx, `SELECT ordinal FROM gateway_conversation_items WHERE owner_key=$1 AND conversation_id=$2 AND id=$3`, owner, conversationID, options.After).Scan(&ordinal)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, conversationstate.ErrNotFound
		}
		if err != nil {
			return nil, false, err
		}
		cursor = &ordinal
	}
	query := `SELECT id,conversation_id,owner_key,payload,ordinal,created_at FROM gateway_conversation_items WHERE owner_key=$1 AND conversation_id=$2 AND ($3::bigint IS NULL OR ordinal>$3) ORDER BY ordinal ASC LIMIT $4`
	if options.Order == "desc" {
		query = `SELECT id,conversation_id,owner_key,payload,ordinal,created_at FROM gateway_conversation_items WHERE owner_key=$1 AND conversation_id=$2 AND ($3::bigint IS NULL OR ordinal<$3) ORDER BY ordinal DESC LIMIT $4`
	}
	rows, err := s.pool.Query(ctx, query, owner, conversationID, cursor, options.Limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := make([]conversationstate.Item, 0, options.Limit+1)
	for rows.Next() {
		item, scanErr := scanConversationItem(rows)
		if scanErr != nil {
			return nil, false, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(items) > options.Limit
	if hasMore {
		items = items[:options.Limit]
	}
	return items, hasMore, nil
}

func (s *PostgresStore) GetItem(ctx context.Context, owner, conversationID, itemID string) (conversationstate.Item, error) {
	if s == nil || s.pool == nil {
		return conversationstate.Item{}, conversationstate.ErrUnavailable
	}
	if owner == "" || conversationID == "" || itemID == "" {
		return conversationstate.Item{}, conversationstate.ErrInvalid
	}
	item, err := scanConversationItem(s.pool.QueryRow(ctx, `SELECT id,conversation_id,owner_key,payload,ordinal,created_at FROM gateway_conversation_items WHERE owner_key=$1 AND conversation_id=$2 AND id=$3`, owner, conversationID, itemID))
	if errors.Is(err, pgx.ErrNoRows) {
		return conversationstate.Item{}, conversationstate.ErrNotFound
	}
	return item, err
}

func (s *PostgresStore) DeleteItem(ctx context.Context, owner, conversationID, itemID string) error {
	if s == nil || s.pool == nil {
		return conversationstate.ErrUnavailable
	}
	if owner == "" || conversationID == "" || itemID == "" {
		return conversationstate.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockConversation(ctx, tx, owner, conversationID); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `DELETE FROM gateway_conversation_items WHERE owner_key=$1 AND conversation_id=$2 AND id=$3`, owner, conversationID, itemID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return conversationstate.ErrNotFound
	}
	if _, err = tx.Exec(ctx, `UPDATE gateway_conversations SET revision=revision+1,updated_at=now() WHERE owner_key=$1 AND id=$2`, owner, conversationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) BeginTurn(ctx context.Context, owner, conversationID, executionID string, lease time.Duration) (conversationstate.Turn, error) {
	if s == nil || s.pool == nil {
		return conversationstate.Turn{}, conversationstate.ErrUnavailable
	}
	if owner == "" || conversationID == "" || executionID == "" || lease <= 0 {
		return conversationstate.Turn{}, conversationstate.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return conversationstate.Turn{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var active *string
	var leaseActive bool
	var durable bool
	conversation, err := scanConversationWithLease(tx.QueryRow(ctx, `SELECT id,owner_key,metadata,revision,created_at,updated_at,active_execution_id,COALESCE(lease_until>now(),false),durable_active FROM gateway_conversations WHERE owner_key=$1 AND id=$2 FOR UPDATE`, owner, conversationID), &active, &leaseActive, &durable)
	if errors.Is(err, pgx.ErrNoRows) {
		return conversationstate.Turn{}, conversationstate.ErrNotFound
	}
	if err != nil {
		return conversationstate.Turn{}, err
	}
	if active != nil && (leaseActive || durable) {
		return conversationstate.Turn{}, conversationstate.ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE gateway_conversations SET active_execution_id=$3,lease_until=now()+$4::interval,durable_active=false WHERE owner_key=$1 AND id=$2`, owner, conversationID, executionID, lease.String()); err != nil {
		return conversationstate.Turn{}, err
	}
	rows, err := tx.Query(ctx, `SELECT id,conversation_id,owner_key,payload,ordinal,created_at FROM gateway_conversation_items WHERE owner_key=$1 AND conversation_id=$2 ORDER BY ordinal ASC`, owner, conversationID)
	if err != nil {
		return conversationstate.Turn{}, err
	}
	defer rows.Close()
	var items []conversationstate.Item
	for rows.Next() {
		item, scanErr := scanConversationItem(rows)
		if scanErr != nil {
			return conversationstate.Turn{}, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return conversationstate.Turn{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return conversationstate.Turn{}, err
	}
	return conversationstate.Turn{Conversation: conversation, Items: items, ExecutionID: executionID}, nil
}

func scanConversationWithLease(row conversationRow, active **string, leaseActive, durable *bool) (conversationstate.Conversation, error) {
	var value conversationstate.Conversation
	err := row.Scan(&value.ID, &value.OwnerKey, &value.Metadata, &value.Revision, &value.CreatedAt, &value.UpdatedAt, active, leaseActive, durable)
	return value, err
}

func (s *PostgresStore) StageTurn(ctx context.Context, turn conversationstate.Turn, items []conversationstate.Item, itemQuota, outputReserve int) error {
	if s == nil || s.pool == nil {
		return conversationstate.ErrUnavailable
	}
	owner, conversationID := turn.Conversation.OwnerKey, turn.Conversation.ID
	if turn.ExecutionID == "" || itemQuota < 1 || len(items) < 1 || !validConversationItems(items, owner, conversationID) {
		return conversationstate.ErrInvalid
	}
	if outputReserve < 1 || outputReserve > itemQuota || len(items) > itemQuota-outputReserve {
		return conversationstate.ErrQuotaExceeded
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var found bool
	if err = tx.QueryRow(ctx, `SELECT true FROM gateway_conversations WHERE owner_key=$1 AND id=$2 AND active_execution_id=$3 FOR UPDATE`, owner, conversationID, turn.ExecutionID).Scan(&found); errors.Is(err, pgx.ErrNoRows) {
		return conversationstate.ErrConflict
	} else if err != nil {
		return err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_conversation_items WHERE owner_key=$1 AND conversation_id=$2`, owner, conversationID).Scan(&count); err != nil {
		return err
	}
	if count > itemQuota-outputReserve-len(items) {
		return conversationstate.ErrQuotaExceeded
	}
	for position, item := range items {
		if _, err = tx.Exec(ctx, `INSERT INTO gateway_conversation_pending_items (execution_id,id,conversation_id,owner_key,payload,position) VALUES ($1,$2,$3,$4,$5::jsonb,$6)`, turn.ExecutionID, item.ID, conversationID, owner, item.Payload, position); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE gateway_conversations SET durable_active=true WHERE owner_key=$1 AND id=$2 AND active_execution_id=$3`, owner, conversationID, turn.ExecutionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) CompleteTurn(ctx context.Context, turn conversationstate.Turn, items []conversationstate.Item, itemQuota int) error {
	if s == nil || s.pool == nil {
		return conversationstate.ErrUnavailable
	}
	owner, conversationID := turn.Conversation.OwnerKey, turn.Conversation.ID
	if turn.ExecutionID == "" || itemQuota < 1 || len(items) < 1 || !validConversationItems(items, owner, conversationID) {
		return conversationstate.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var active, last *string
	var lastCommitted *bool
	err = tx.QueryRow(ctx, `SELECT active_execution_id,last_execution_id,last_execution_committed FROM gateway_conversations WHERE owner_key=$1 AND id=$2 FOR UPDATE`, owner, conversationID).Scan(&active, &last, &lastCommitted)
	if errors.Is(err, pgx.ErrNoRows) {
		return conversationstate.ErrNotFound
	}
	if err != nil {
		return err
	}
	if last != nil && *last == turn.ExecutionID && lastCommitted != nil && *lastCommitted {
		return tx.Commit(ctx)
	}
	if active == nil {
		return conversationstate.ErrConflict
	}
	if *active != turn.ExecutionID {
		return conversationstate.ErrConflict
	}
	rows, err := tx.Query(ctx, `SELECT id,conversation_id,owner_key,payload,0,created_at FROM gateway_conversation_pending_items WHERE owner_key=$1 AND conversation_id=$2 AND execution_id=$3 ORDER BY position ASC`, owner, conversationID, turn.ExecutionID)
	if err != nil {
		return err
	}
	var pending []conversationstate.Item
	for rows.Next() {
		item, scanErr := scanConversationItem(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		pending = append(pending, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	items = append(pending, items...)
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_conversation_items WHERE owner_key=$1 AND conversation_id=$2`, owner, conversationID).Scan(&count); err != nil {
		return err
	}
	if count > itemQuota-len(items) {
		return conversationstate.ErrQuotaExceeded
	}
	if _, err = insertConversationItems(ctx, tx, items); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM gateway_conversation_pending_items WHERE owner_key=$1 AND conversation_id=$2 AND execution_id=$3`, owner, conversationID, turn.ExecutionID); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `UPDATE gateway_conversations SET active_execution_id=NULL,lease_until=NULL,durable_active=false,last_execution_id=$3,last_execution_committed=true,revision=revision+1,updated_at=now() WHERE owner_key=$1 AND id=$2 AND active_execution_id=$3`, owner, conversationID, turn.ExecutionID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return conversationstate.ErrConflict
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) ReleaseTurn(ctx context.Context, turn conversationstate.Turn) error {
	if s == nil || s.pool == nil {
		return conversationstate.ErrUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var active, last *string
	var lastCommitted *bool
	err = tx.QueryRow(ctx, `SELECT active_execution_id,last_execution_id,last_execution_committed FROM gateway_conversations WHERE owner_key=$1 AND id=$2 FOR UPDATE`, turn.Conversation.OwnerKey, turn.Conversation.ID).Scan(&active, &last, &lastCommitted)
	if errors.Is(err, pgx.ErrNoRows) {
		return conversationstate.ErrNotFound
	}
	if err != nil {
		return err
	}
	if last != nil && *last == turn.ExecutionID && lastCommitted != nil && !*lastCommitted {
		return tx.Commit(ctx)
	}
	if active == nil || *active != turn.ExecutionID {
		return conversationstate.ErrConflict
	}
	if _, err = tx.Exec(ctx, `DELETE FROM gateway_conversation_pending_items WHERE owner_key=$1 AND conversation_id=$2 AND execution_id=$3`, turn.Conversation.OwnerKey, turn.Conversation.ID, turn.ExecutionID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE gateway_conversations SET active_execution_id=NULL,lease_until=NULL,durable_active=false,last_execution_id=$3,last_execution_committed=false WHERE owner_key=$1 AND id=$2 AND active_execution_id=$3`, turn.Conversation.OwnerKey, turn.Conversation.ID, turn.ExecutionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func lockConversation(ctx context.Context, tx pgx.Tx, owner, conversationID string) error {
	var active *string
	var leaseActive bool
	var durable bool
	err := tx.QueryRow(ctx, `SELECT active_execution_id,COALESCE(lease_until>now(),false),durable_active FROM gateway_conversations WHERE owner_key=$1 AND id=$2 FOR UPDATE`, owner, conversationID).Scan(&active, &leaseActive, &durable)
	if errors.Is(err, pgx.ErrNoRows) {
		return conversationstate.ErrNotFound
	}
	if err != nil {
		return err
	}
	if active != nil && (leaseActive || durable) {
		return conversationstate.ErrConflict
	}
	if active != nil {
		if _, err := tx.Exec(ctx, `UPDATE gateway_conversations SET active_execution_id=NULL,lease_until=NULL,durable_active=false WHERE owner_key=$1 AND id=$2`, owner, conversationID); err != nil {
			return err
		}
	}
	return nil
}

func insertConversationItems(ctx context.Context, tx pgx.Tx, items []conversationstate.Item) ([]conversationstate.Item, error) {
	created := make([]conversationstate.Item, 0, len(items))
	for _, item := range items {
		value, err := scanConversationItem(tx.QueryRow(ctx, `INSERT INTO gateway_conversation_items (id,conversation_id,owner_key,payload) VALUES ($1,$2,$3,$4::jsonb) ON CONFLICT DO NOTHING RETURNING id,conversation_id,owner_key,payload,ordinal,created_at`, item.ID, item.ConversationID, item.OwnerKey, item.Payload))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, conversationstate.ErrConflict
		}
		if err != nil {
			return nil, err
		}
		created = append(created, value)
	}
	return created, nil
}
