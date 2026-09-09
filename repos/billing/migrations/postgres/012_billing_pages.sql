ALTER TABLE billing_budget_reservations
    ADD COLUMN IF NOT EXISTS page_cost_per_1k NUMERIC(18, 8) NOT NULL DEFAULT 0;
