CREATE TABLE IF NOT EXISTS billing_budget_policies
(
    id          BIGSERIAL PRIMARY KEY,
    scope_type  TEXT        NOT NULL CHECK (scope_type IN ('global', 'key', 'user', 'team', 'model', 'provider')),
    scope_id    TEXT        NOT NULL,
    period      TEXT        NOT NULL CHECK (period IN ('hour', 'day', 'week', 'month')),
    currency    TEXT        NOT NULL DEFAULT 'USD',
    max_cost    NUMERIC(18, 8),
    max_tokens  BIGINT,
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (COALESCE(max_cost, 0) > 0 OR COALESCE(max_tokens, 0) > 0)
);

CREATE INDEX IF NOT EXISTS idx_billing_budget_policies_scope
    ON billing_budget_policies(enabled, currency, scope_type, scope_id);

CREATE TABLE IF NOT EXISTS billing_budget_reservations
(
    request_id             TEXT PRIMARY KEY,
    owner_key              TEXT           NOT NULL,
    credential_id          TEXT           NOT NULL DEFAULT '',
    user_id                TEXT           NOT NULL DEFAULT '',
    team_id                TEXT           NOT NULL DEFAULT '',
    provider_name          TEXT           NOT NULL DEFAULT '',
    provider_type          TEXT           NOT NULL DEFAULT '',
    model                  TEXT           NOT NULL DEFAULT '',
    currency               TEXT           NOT NULL DEFAULT 'USD',
    state                  TEXT           NOT NULL CHECK (state IN ('reserved', 'committed', 'canceled')),
    reserved_cost          NUMERIC(18, 8) NOT NULL DEFAULT 0,
    reserved_tokens        BIGINT         NOT NULL DEFAULT 0,
    actual_cost            NUMERIC(18, 8) NOT NULL DEFAULT 0,
    actual_tokens          BIGINT         NOT NULL DEFAULT 0,
    reservation_expires_at TIMESTAMPTZ    NOT NULL,
    created_at             TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ    NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_billing_budget_reservations_active
    ON billing_budget_reservations(created_at, reservation_expires_at, state);

CREATE INDEX IF NOT EXISTS idx_billing_budget_reservations_identity
    ON billing_budget_reservations(credential_id, user_id, team_id, model, provider_name, provider_type);
