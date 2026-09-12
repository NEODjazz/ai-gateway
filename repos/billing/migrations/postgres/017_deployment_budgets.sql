ALTER TABLE billing_budget_policies
    DROP CONSTRAINT IF EXISTS billing_budget_policies_scope_type_check;

ALTER TABLE billing_budget_policies
    ADD CONSTRAINT billing_budget_policies_scope_type_check
    CHECK (scope_type IN ('global', 'key', 'user', 'team', 'organization', 'model', 'provider', 'deployment', 'tag'));

ALTER TABLE billing_budget_reservations
    ADD COLUMN IF NOT EXISTS deployment_name TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_billing_budget_reservations_deployment
    ON billing_budget_reservations(deployment_name, created_at, state);
