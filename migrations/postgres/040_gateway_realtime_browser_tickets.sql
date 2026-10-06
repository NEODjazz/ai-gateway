CREATE TABLE IF NOT EXISTS gateway_realtime_browser_tickets (
    ticket_hash CHAR(64) PRIMARY KEY CHECK (ticket_hash ~ '^[0-9a-f]{64}$'),
    owner_key CHAR(64) NOT NULL CHECK (owner_key ~ '^[0-9a-f]{64}$'),
    model TEXT NOT NULL CHECK (length(model) BETWEEN 1 AND 256),
    origin TEXT NOT NULL CHECK (length(origin) BETWEEN 1 AND 2048),
    payload BYTEA NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 16384),
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS gateway_realtime_browser_tickets_expiry ON gateway_realtime_browser_tickets(expires_at);
CREATE INDEX IF NOT EXISTS gateway_realtime_browser_tickets_owner ON gateway_realtime_browser_tickets(owner_key);
