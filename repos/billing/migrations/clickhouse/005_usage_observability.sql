ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS organization_id String AFTER team_id;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS first_token_latency_ms UInt32 AFTER latency_ms;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS retry_count UInt16 AFTER first_token_latency_ms;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS fallback_count UInt16 AFTER retry_count;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS cache_kind LowCardinality(String) AFTER cache_status;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS usage_estimated Bool AFTER total_tokens;
