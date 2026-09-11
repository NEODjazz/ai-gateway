ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS provider_cost_usd_ticks Int64 AFTER cost;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS provider_cost_reported Bool AFTER provider_cost_usd_ticks;
