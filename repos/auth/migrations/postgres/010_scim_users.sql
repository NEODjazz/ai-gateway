ALTER TABLE users
    ADD COLUMN IF NOT EXISTS external_id TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS auth_users_external_id_idx
    ON users (external_id)
    WHERE external_id <> '';

CREATE UNIQUE INDEX IF NOT EXISTS auth_users_username_idx
    ON users (lower(email))
    WHERE email IS NOT NULL AND email <> '';
