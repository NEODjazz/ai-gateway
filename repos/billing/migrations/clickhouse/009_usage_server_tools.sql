ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS search_requests UInt32 AFTER cache_write_input_tokens;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS search_requests_estimated Bool AFTER search_requests;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS search_cost_per_1k Float64 AFTER output_cost_per_1m;
