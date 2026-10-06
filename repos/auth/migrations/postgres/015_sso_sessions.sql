CREATE TABLE IF NOT EXISTS auth_sso_sessions (
    token_hash CHAR(64) PRIMARY KEY,
    profile_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    payload BYTEA NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 8192),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS auth_sso_sessions_expiry ON auth_sso_sessions(expires_at);
CREATE INDEX IF NOT EXISTS auth_sso_sessions_user ON auth_sso_sessions(user_id);

CREATE TABLE IF NOT EXISTS auth_sso_logins (
    nonce_hash CHAR(64) PRIMARY KEY,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS auth_sso_logins_expiry ON auth_sso_logins(expires_at);
