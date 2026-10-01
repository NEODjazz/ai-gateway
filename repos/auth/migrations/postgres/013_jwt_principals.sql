CREATE TABLE IF NOT EXISTS auth_jwt_principals
(
    issuer TEXT NOT NULL CHECK (length(issuer) BETWEEN 1 AND 2048),
    subject TEXT NOT NULL CHECK (length(subject) BETWEEN 1 AND 256),
    audience TEXT NOT NULL CHECK (length(audience) BETWEEN 1 AND 256),
    user_id TEXT NOT NULL REFERENCES users(id),
    team_id TEXT REFERENCES auth_teams(id),
    tags TEXT[] NOT NULL DEFAULT '{}',
    access_group_ids TEXT[] NOT NULL DEFAULT '{}',
    allowed_models TEXT[] NOT NULL DEFAULT '{}',
    allowed_tools TEXT[] NOT NULL DEFAULT '{}',
    rate_limit_rpm INTEGER NOT NULL DEFAULT 0 CHECK (rate_limit_rpm >= 0),
    rate_limit_tpm INTEGER NOT NULL DEFAULT 0 CHECK (rate_limit_tpm >= 0),
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (issuer, subject, audience)
);
CREATE INDEX IF NOT EXISTS auth_jwt_principals_user_idx ON auth_jwt_principals(user_id);
