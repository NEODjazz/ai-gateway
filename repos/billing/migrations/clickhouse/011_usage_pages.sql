ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS input_pages UInt32 AFTER input_characters;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS page_cost_per_1k Float64 AFTER character_cost_per_1m;
