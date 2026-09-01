ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS trace_id String AFTER session_id;
