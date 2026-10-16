CREATE TABLE IF NOT EXISTS auth_sso_connections (
    id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 128),
    provider TEXT NOT NULL CHECK (provider IN ('entra','keycloak','oidc')),
    organization_id TEXT REFERENCES auth_organizations(id),
    revision BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0),
    payload BYTEA NOT NULL DEFAULT '' CHECK (octet_length(payload) <= 65536),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
