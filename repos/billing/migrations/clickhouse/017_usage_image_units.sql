ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS output_images UInt32 AFTER video_seconds;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS image_cost_per_unit Float64 AFTER video_cost_per_second;
