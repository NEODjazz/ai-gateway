ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS team_id String AFTER user_id;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS catalog_version String AFTER currency;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS pricing_key String AFTER catalog_version;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS input_cost_per_1m Float64 AFTER pricing_key;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS output_cost_per_1m Float64 AFTER input_cost_per_1m;
