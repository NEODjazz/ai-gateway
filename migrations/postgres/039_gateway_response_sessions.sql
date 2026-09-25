CREATE TABLE IF NOT EXISTS gateway_response_sessions (
    key TEXT PRIMARY KEY CHECK (length(key) BETWEEN 1 AND 256),
    value BYTEA NOT NULL CHECK (octet_length(value) BETWEEN 1 AND 4096),
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS gateway_response_sessions_expires_at_idx
    ON gateway_response_sessions (expires_at);
