ALTER TABLE billing_budget_reservations
    ADD COLUMN IF NOT EXISTS video_cost_per_second numeric(20,10) NOT NULL DEFAULT 0;
