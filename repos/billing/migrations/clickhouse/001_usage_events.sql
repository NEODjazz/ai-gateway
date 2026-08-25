CREATE DATABASE IF NOT EXISTS ai_gateway;

CREATE TABLE IF NOT EXISTS ai_gateway.usage_events
(
    timestamp String,
    timestamp_unix UInt64 DEFAULT toUnixTimestamp(parseDateTimeBestEffort(timestamp)),
    event_id String,
    request_id String,
    user_id String,
    roles Array(String),
    api_key_fingerprint String,
    provider String,
    provider_endpoint_name String,
    provider_endpoint_type String,
    model String,
    api_type LowCardinality(String),
    phase LowCardinality(String),
    cache_status LowCardinality(String),
    status LowCardinality(String),
    error String,
    failure_class LowCardinality(String),
    latency_ms UInt32,
    prompt_tokens_estimated UInt32,
    input_tokens UInt32,
    output_tokens UInt32,
    total_tokens UInt32,
    cost Float64,
    currency LowCardinality(String)
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(parseDateTimeBestEffort(timestamp))
ORDER BY (user_id, provider, model, timestamp)
TTL parseDateTimeBestEffort(timestamp) + INTERVAL 24 MONTH;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS timestamp String DEFAULT formatDateTime(toDateTime(timestamp_unix), '%Y-%m-%dT%H:%i:%SZ') FIRST;

ALTER TABLE ai_gateway.usage_events
    MODIFY COLUMN IF EXISTS timestamp_unix UInt64 DEFAULT toUnixTimestamp(parseDateTimeBestEffort(timestamp));

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS event_id String AFTER timestamp_unix;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS phase LowCardinality(String) AFTER api_type;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS cache_status LowCardinality(String) AFTER phase;

ALTER TABLE ai_gateway.usage_events
    ADD COLUMN IF NOT EXISTS failure_class LowCardinality(String) AFTER error;
