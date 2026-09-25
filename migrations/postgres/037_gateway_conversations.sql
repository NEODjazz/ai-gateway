CREATE TABLE IF NOT EXISTS gateway_conversations (
    id TEXT NOT NULL,
    owner_key TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    revision BIGINT NOT NULL DEFAULT 1,
    active_execution_id TEXT,
    lease_until TIMESTAMPTZ,
    durable_active BOOLEAN NOT NULL DEFAULT false,
    last_execution_id TEXT,
    last_execution_committed BOOLEAN,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (length(id) BETWEEN 1 AND 128),
    CHECK (length(owner_key) BETWEEN 1 AND 256),
    CHECK (jsonb_typeof(metadata) = 'object'),
    CHECK (octet_length(metadata::text) BETWEEN 2 AND 65536),
    CHECK (revision > 0),
    CHECK ((active_execution_id IS NULL) = (lease_until IS NULL)),
    CHECK (active_execution_id IS NOT NULL OR durable_active = false),
    CHECK ((last_execution_id IS NULL) = (last_execution_committed IS NULL)),
    CHECK (last_execution_id IS NULL OR length(last_execution_id) BETWEEN 1 AND 128),
    CHECK (active_execution_id IS NULL OR length(active_execution_id) BETWEEN 1 AND 128),
    PRIMARY KEY (owner_key,id)
);

CREATE TABLE IF NOT EXISTS gateway_conversation_pending_items (
    execution_id TEXT NOT NULL,
    id TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    owner_key TEXT NOT NULL,
    payload JSONB NOT NULL,
    position INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (length(execution_id) BETWEEN 1 AND 128),
    CHECK (length(id) BETWEEN 1 AND 128),
    CHECK (length(conversation_id) BETWEEN 1 AND 128),
    CHECK (length(owner_key) BETWEEN 1 AND 256),
    CHECK (jsonb_typeof(payload) = 'object'),
    CHECK (octet_length(payload::text) BETWEEN 2 AND 2097152),
    CHECK (position >= 0),
    PRIMARY KEY (owner_key,conversation_id,execution_id,id),
    UNIQUE (owner_key,conversation_id,execution_id,position),
    FOREIGN KEY (owner_key,conversation_id) REFERENCES gateway_conversations(owner_key,id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS gateway_conversations_owner_created_idx
    ON gateway_conversations (owner_key,created_at DESC,id DESC);

CREATE TABLE IF NOT EXISTS gateway_conversation_items (
    id TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    owner_key TEXT NOT NULL,
    payload JSONB NOT NULL,
    ordinal BIGSERIAL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (length(id) BETWEEN 1 AND 128),
    CHECK (length(conversation_id) BETWEEN 1 AND 128),
    CHECK (length(owner_key) BETWEEN 1 AND 256),
    CHECK (jsonb_typeof(payload) = 'object'),
    CHECK (octet_length(payload::text) BETWEEN 2 AND 2097152),
    PRIMARY KEY (owner_key,conversation_id,id),
	UNIQUE (owner_key,conversation_id,ordinal),
    FOREIGN KEY (owner_key,conversation_id) REFERENCES gateway_conversations(owner_key,id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS gateway_conversation_items_created_idx
    ON gateway_conversation_items (owner_key,conversation_id,ordinal);
