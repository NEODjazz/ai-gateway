CREATE TABLE IF NOT EXISTS gateway_control_plane_state
(
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    revision BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0),
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO gateway_control_plane_state (singleton, revision, payload)
VALUES (TRUE, 0, '{}'::jsonb)
ON CONFLICT (singleton) DO NOTHING;
