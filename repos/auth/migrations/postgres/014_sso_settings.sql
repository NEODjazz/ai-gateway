CREATE TABLE IF NOT EXISTS auth_sso_settings
(
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    revision BIGINT NOT NULL CHECK (revision > 0),
    payload BYTEA NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 65536),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
