ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS cache_read_input_tokens UInt32 AFTER total_tokens;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS cache_write_input_tokens UInt32 AFTER cache_read_input_tokens;
