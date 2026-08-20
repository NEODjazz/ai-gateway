CREATE TABLE IF NOT EXISTS billing_event_ledger
(
    event_id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL,
    phase TEXT NOT NULL CHECK (phase IN ('reserve', 'commit', 'cancel')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS billing_outbox
(
    id BIGSERIAL PRIMARY KEY,
    event_id TEXT NOT NULL UNIQUE REFERENCES billing_event_ledger(event_id),
    payload JSONB NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_at TIMESTAMPTZ,
    delivered_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_billing_outbox_pending
    ON billing_outbox(available_at, id)
    WHERE delivered_at IS NULL;
