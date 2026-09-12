ALTER TABLE users
    ADD COLUMN IF NOT EXISTS external_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS scim_deleted_at TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS auth_users_external_id_active_idx
    ON users (external_id)
    WHERE external_id <> '' AND scim_deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS auth_users_username_active_idx
    ON users (lower(email))
    WHERE email IS NOT NULL AND email <> '' AND scim_deleted_at IS NULL;
