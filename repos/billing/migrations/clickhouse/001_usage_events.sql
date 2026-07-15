CREATE DATABASE IF NOT EXISTS ai_gateway;

CREATE TABLE IF NOT EXISTS ai_gateway.usage_events
(
    timestamp String,
    timestamp_unix UInt64 DEFAULT toUnixTimestamp(parseDateTimeBestEffort(timestamp)),
    request_id String,
    user_id String,
    roles Array(String),
    api_key_fingerprint String,
    provider String,
    provider_endpoint_name String,
    provider_endpoint_type String,
    model String,
    api_type LowCardinality(String),
    status LowCardinality(String),
    error String,
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
