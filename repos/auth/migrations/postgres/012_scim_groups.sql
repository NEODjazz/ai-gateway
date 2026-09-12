ALTER TABLE auth_teams
    ADD COLUMN IF NOT EXISTS external_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS scim_deleted_at TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS auth_teams_external_id_active_idx
    ON auth_teams (external_id)
    WHERE external_id <> '' AND scim_deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS auth_teams_name_active_idx
    ON auth_teams (lower(name))
    WHERE scim_deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS auth_teams_scim_visible_idx
    ON auth_teams (created_at DESC, id)
    WHERE scim_deleted_at IS NULL;
