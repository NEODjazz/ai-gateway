ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS tags Array(String) AFTER roles;
