ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS input_characters UInt32 AFTER total_tokens;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS character_cost_per_1m Float64 AFTER search_cost_per_1k;
