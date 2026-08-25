ALTER TABLE auth_virtual_keys
    ADD COLUMN IF NOT EXISTS alias TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS tags TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS disabled_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS auth_virtual_keys_disabled_at_idx
    ON auth_virtual_keys (disabled_at)
    WHERE disabled_at IS NOT NULL AND revoked_at IS NULL;
