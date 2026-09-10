ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS tool_requests UInt32 AFTER input_audio_milliseconds;
