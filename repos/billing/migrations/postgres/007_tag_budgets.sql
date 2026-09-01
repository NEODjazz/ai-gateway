ALTER TABLE billing_budget_policies
    DROP CONSTRAINT IF EXISTS billing_budget_policies_scope_type_check;

ALTER TABLE billing_budget_policies
    ADD CONSTRAINT billing_budget_policies_scope_type_check
    CHECK (scope_type IN ('global', 'key', 'user', 'team', 'model', 'provider', 'tag'));

ALTER TABLE billing_budget_reservations
    ADD COLUMN IF NOT EXISTS tags TEXT[] NOT NULL DEFAULT '{}';

CREATE INDEX IF NOT EXISTS idx_billing_budget_reservations_tags
    ON billing_budget_reservations USING GIN(tags);
