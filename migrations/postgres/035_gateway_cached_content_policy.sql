ALTER TABLE gateway_cached_contents
    ADD COLUMN IF NOT EXISTS policy_fingerprint CHAR(64) NOT NULL DEFAULT repeat('0',64);

ALTER TABLE gateway_cached_contents
    ALTER COLUMN policy_fingerprint DROP DEFAULT;

ALTER TABLE gateway_cached_contents
    DROP CONSTRAINT IF EXISTS gateway_cached_contents_policy_fingerprint_check;

ALTER TABLE gateway_cached_contents
    ADD CONSTRAINT gateway_cached_contents_policy_fingerprint_check
    CHECK (length(policy_fingerprint) = 64);
