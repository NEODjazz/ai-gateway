CREATE TABLE IF NOT EXISTS auth_organizations
(
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS auth_organization_teams
(
    organization_id TEXT NOT NULL REFERENCES auth_organizations(id) ON DELETE CASCADE,
    team_id TEXT NOT NULL REFERENCES auth_teams(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, team_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS auth_organization_teams_team_idx
    ON auth_organization_teams (team_id);
