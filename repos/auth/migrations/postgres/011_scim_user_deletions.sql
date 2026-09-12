ALTER TABLE users
    ADD COLUMN IF NOT EXISTS scim_deleted_at TIMESTAMPTZ;

DROP INDEX IF EXISTS auth_users_external_id_idx;
DROP INDEX IF EXISTS auth_users_username_idx;

CREATE INDEX IF NOT EXISTS auth_users_scim_visible_idx
    ON users (created_at DESC, id)
    WHERE scim_deleted_at IS NULL;
