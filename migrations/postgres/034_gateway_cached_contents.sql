CREATE TABLE IF NOT EXISTS gateway_cached_contents (
    cached_content_name TEXT NOT NULL,
    owner_key TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    model TEXT NOT NULL,
    deployment CHAR(64) NOT NULL,
    snapshot JSONB NOT NULL CHECK (jsonb_typeof(snapshot) = 'object'),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_key,cached_content_name),
    CHECK (length(cached_content_name) BETWEEN 16 AND 256),
    CHECK (length(owner_key) BETWEEN 1 AND 256),
    CHECK (length(endpoint) BETWEEN 1 AND 128),
    CHECK (length(model) BETWEEN 1 AND 256),
    CHECK (length(deployment) = 64),
    CHECK (octet_length(snapshot::text) BETWEEN 2 AND 1048576)
);
CREATE INDEX IF NOT EXISTS gateway_cached_contents_owner_created_idx
    ON gateway_cached_contents (owner_key,created_at DESC,cached_content_name DESC);
CREATE INDEX IF NOT EXISTS gateway_cached_contents_expiry_idx
    ON gateway_cached_contents (expires_at);
