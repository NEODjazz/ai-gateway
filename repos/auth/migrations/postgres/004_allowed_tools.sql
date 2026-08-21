ALTER TABLE auth_virtual_keys
    ADD COLUMN IF NOT EXISTS allowed_tools TEXT[] NOT NULL DEFAULT '{}';
