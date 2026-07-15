CREATE TABLE IF NOT EXISTS users
(
    id TEXT PRIMARY KEY,
    email TEXT,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS tariffs
(
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    currency TEXT NOT NULL DEFAULT 'USD',
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS tariff_prices
(
    id BIGSERIAL PRIMARY KEY,
    tariff_id TEXT NOT NULL REFERENCES tariffs(id),
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    input_price_per_1k NUMERIC(18, 8) NOT NULL DEFAULT 0,
    output_price_per_1k NUMERIC(18, 8) NOT NULL DEFAULT 0,
    effective_from TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS user_tariffs
(
    user_id TEXT NOT NULL REFERENCES users(id),
    tariff_id TEXT NOT NULL REFERENCES tariffs(id),
    active_from TIMESTAMPTZ NOT NULL DEFAULT now(),
    active_to TIMESTAMPTZ,
    PRIMARY KEY (user_id, tariff_id, active_from)
);

CREATE TABLE IF NOT EXISTS balances
(
    user_id TEXT PRIMARY KEY REFERENCES users(id),
    currency TEXT NOT NULL DEFAULT 'USD',
    amount NUMERIC(18, 8) NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS invoices
(
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id),
    currency TEXT NOT NULL DEFAULT 'USD',
    amount NUMERIC(18, 8) NOT NULL,
    status TEXT NOT NULL DEFAULT 'draft',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS payments
(
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id),
    invoice_id TEXT REFERENCES invoices(id),
    currency TEXT NOT NULL DEFAULT 'USD',
    amount NUMERIC(18, 8) NOT NULL,
    status TEXT NOT NULL,
    provider TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS limits
(
    id BIGSERIAL PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id),
    provider TEXT,
    model TEXT,
    max_input_tokens_per_day BIGINT,
    max_output_tokens_per_day BIGINT,
    max_total_tokens_per_day BIGINT,
    max_cost_per_day NUMERIC(18, 8),
    enabled BOOLEAN NOT NULL DEFAULT true
);

CREATE TABLE IF NOT EXISTS quotas
(
    id BIGSERIAL PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id),
    period TEXT NOT NULL,
    provider TEXT,
    model TEXT,
    total_tokens BIGINT NOT NULL DEFAULT 0,
    cost NUMERIC(18, 8) NOT NULL DEFAULT 0,
    enabled BOOLEAN NOT NULL DEFAULT true
);

CREATE TABLE IF NOT EXISTS financial_transactions
(
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id),
    request_id TEXT,
    transaction_type TEXT NOT NULL,
    currency TEXT NOT NULL DEFAULT 'USD',
    amount NUMERIC(18, 8) NOT NULL,
    balance_after NUMERIC(18, 8),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_financial_transactions_user_created
    ON financial_transactions(user_id, created_at DESC);
