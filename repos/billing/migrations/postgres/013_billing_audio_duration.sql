ALTER TABLE billing_budget_reservations
    ADD COLUMN IF NOT EXISTS audio_cost_per_minute NUMERIC(18, 8) NOT NULL DEFAULT 0;
