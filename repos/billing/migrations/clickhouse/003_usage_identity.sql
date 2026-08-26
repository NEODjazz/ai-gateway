ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS provider_id String AFTER provider;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS upstream_model String AFTER model;
