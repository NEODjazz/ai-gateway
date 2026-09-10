CREATE TABLE IF NOT EXISTS gateway_mcp_tool_calls
(
    scope_key       TEXT        NOT NULL,
    idempotency_key TEXT        NOT NULL,
    request_hash    TEXT        NOT NULL,
    execution_id    TEXT        NOT NULL,
    state           TEXT        NOT NULL CHECK (state IN ('pending', 'completed')),
    http_status     INTEGER     NOT NULL DEFAULT 0 CHECK (http_status BETWEEN 0 AND 599),
    response        BYTEA,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (scope_key, idempotency_key),
    CHECK (length(scope_key) BETWEEN 1 AND 1024),
    CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    CHECK (length(request_hash) = 64),
    CHECK (length(execution_id) BETWEEN 1 AND 128),
    CHECK ((state = 'pending' AND http_status = 0 AND response IS NULL)
        OR (state = 'completed' AND http_status BETWEEN 100 AND 599 AND response IS NOT NULL))
);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'gateway_mcp_tool_calls'
          AND column_name = 'response'
          AND data_type <> 'bytea'
    ) THEN
        ALTER TABLE gateway_mcp_tool_calls
            ALTER COLUMN response TYPE BYTEA USING convert_to(response::text, 'UTF8');
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS gateway_mcp_tool_calls_updated_at_idx
    ON gateway_mcp_tool_calls (updated_at);

DELETE FROM gateway_mcp_tool_calls WHERE updated_at < now() - interval '24 hours';
