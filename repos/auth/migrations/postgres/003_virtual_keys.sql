CREATE TABLE IF NOT EXISTS auth_virtual_keys
(
    id TEXT PRIMARY KEY,
    token_hash CHAR(64) NOT NULL UNIQUE,
    user_id TEXT NOT NULL,
    team_id TEXT,
    roles TEXT[] NOT NULL DEFAULT '{}',
    allowed_models TEXT[] NOT NULL DEFAULT '{}',
    rate_limit_rpm INTEGER NOT NULL DEFAULT 0 CHECK (rate_limit_rpm >= 0),
    rate_limit_tpm INTEGER NOT NULL DEFAULT 0 CHECK (rate_limit_tpm >= 0),
    rotation_family_id TEXT NOT NULL,
    rotated_from_id TEXT REFERENCES auth_virtual_keys(id),
    rotated_to_id TEXT REFERENCES auth_virtual_keys(id),
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_auth_virtual_keys_active_hash
    ON auth_virtual_keys(token_hash)
    WHERE revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_auth_virtual_keys_user
    ON auth_virtual_keys(user_id, created_at DESC);
