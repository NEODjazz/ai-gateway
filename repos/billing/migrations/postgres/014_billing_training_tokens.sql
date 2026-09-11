ALTER TABLE billing_budget_reservations
    ADD COLUMN IF NOT EXISTS training_cost_per_1m NUMERIC(18, 8) NOT NULL DEFAULT 0;
