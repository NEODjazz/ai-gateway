CREATE TABLE IF NOT EXISTS management_audit_events
(
    id BIGSERIAL PRIMARY KEY,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    request_id TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    actor_credential_id TEXT NOT NULL,
    action TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id TEXT NOT NULL DEFAULT '',
    outcome TEXT NOT NULL CHECK (outcome IN ('attempted', 'succeeded', 'failed')),
    details JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS idx_management_audit_events_time
    ON management_audit_events(occurred_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_management_audit_events_actor
    ON management_audit_events(actor_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_management_audit_events_action
    ON management_audit_events(action, id DESC);
