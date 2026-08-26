ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS session_id String AFTER request_id;
