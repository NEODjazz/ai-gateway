ALTER TABLE gateway_conversations
    ADD COLUMN IF NOT EXISTS durable_active BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS last_execution_id TEXT,
    ADD COLUMN IF NOT EXISTS last_execution_committed BOOLEAN;

ALTER TABLE gateway_conversations
    DROP CONSTRAINT IF EXISTS gateway_conversations_durable_active_check,
    DROP CONSTRAINT IF EXISTS gateway_conversations_last_execution_check,
    DROP CONSTRAINT IF EXISTS gateway_conversations_last_execution_id_check;

ALTER TABLE gateway_conversations
    ADD CONSTRAINT gateway_conversations_durable_active_check
        CHECK (active_execution_id IS NOT NULL OR durable_active = false),
    ADD CONSTRAINT gateway_conversations_last_execution_check
        CHECK ((last_execution_id IS NULL) = (last_execution_committed IS NULL)),
    ADD CONSTRAINT gateway_conversations_last_execution_id_check
        CHECK (last_execution_id IS NULL OR length(last_execution_id) BETWEEN 1 AND 128);

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
