CREATE TABLE IF NOT EXISTS auth_organization_memberships (
    organization_id TEXT NOT NULL REFERENCES auth_organizations(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    roles TEXT[] NOT NULL DEFAULT '{user}' CHECK (roles <@ ARRAY['user','developer','org_admin']::TEXT[] AND cardinality(roles) BETWEEN 1 AND 3),
    status TEXT NOT NULL CHECK (status IN ('active','disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, user_id)
);
CREATE INDEX IF NOT EXISTS auth_organization_memberships_user ON auth_organization_memberships(user_id);

DO $$
BEGIN
    LOCK TABLE auth_jwt_principals IN ACCESS EXCLUSIVE MODE;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='auth_jwt_principals' AND column_name='organization_id') THEN
        ALTER TABLE auth_jwt_principals ADD COLUMN organization_id TEXT REFERENCES auth_organizations(id);
        UPDATE auth_jwt_principals p SET organization_id=ot.organization_id
        FROM auth_organization_teams ot WHERE p.team_id=ot.team_id;
    END IF;
END $$;
CREATE INDEX IF NOT EXISTS auth_jwt_principals_organization ON auth_jwt_principals(organization_id);
