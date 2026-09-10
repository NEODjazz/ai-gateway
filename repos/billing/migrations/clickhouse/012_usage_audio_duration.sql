ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS input_audio_milliseconds UInt32 AFTER input_pages;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS audio_cost_per_minute Float64 AFTER page_cost_per_1k;
