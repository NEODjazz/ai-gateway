CREATE TABLE IF NOT EXISTS auth_api_issuers (
    id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 128),
    organization_id TEXT REFERENCES auth_organizations(id),
    issuer TEXT NOT NULL CHECK (length(issuer) BETWEEN 1 AND 2048),
    audience TEXT NOT NULL CHECK (length(audience) BETWEEN 1 AND 256),
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    payload BYTEA NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 65536),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (issuer, audience)
);
