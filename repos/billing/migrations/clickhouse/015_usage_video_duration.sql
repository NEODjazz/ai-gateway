ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS video_seconds UInt32 AFTER input_audio_milliseconds;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS video_cost_per_second Float64 AFTER audio_cost_per_minute;
