ALTER TABLE billing_budget_policies
    ALTER COLUMN max_cost TYPE NUMERIC(20, 10);

ALTER TABLE billing_budget_reservations
    ALTER COLUMN reserved_cost TYPE NUMERIC(20, 10),
    ALTER COLUMN actual_cost TYPE NUMERIC(20, 10);
