ALTER TABLE billing_budget_reservations
    ADD COLUMN IF NOT EXISTS image_cost_per_unit numeric(20,10) NOT NULL DEFAULT 0;
