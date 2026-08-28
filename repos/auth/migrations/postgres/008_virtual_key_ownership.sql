ALTER TABLE auth_virtual_keys
    ALTER COLUMN user_id DROP NOT NULL;

ALTER TABLE auth_virtual_keys
    ADD COLUMN IF NOT EXISTS organization_id TEXT REFERENCES auth_organizations(id);

CREATE INDEX IF NOT EXISTS auth_virtual_keys_organization_idx
    ON auth_virtual_keys (organization_id, created_at DESC);

