ALTER TABLE auth_virtual_keys
    ADD COLUMN IF NOT EXISTS access_group_ids TEXT[] NOT NULL DEFAULT '{}';

CREATE INDEX IF NOT EXISTS auth_virtual_keys_access_groups_idx
    ON auth_virtual_keys USING GIN (access_group_ids);
