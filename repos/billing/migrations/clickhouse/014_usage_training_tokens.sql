ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS training_tokens UInt64 AFTER total_tokens;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS training_cost_per_1m Float64 AFTER output_cost_per_1m;
