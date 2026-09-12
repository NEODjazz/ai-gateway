ALTER TABLE billing_budget_policies
    DROP CONSTRAINT IF EXISTS billing_budget_policies_scope_type_check;

ALTER TABLE billing_budget_policies
    ADD CONSTRAINT billing_budget_policies_scope_type_check
    CHECK (scope_type IN ('global', 'key', 'user', 'team', 'organization', 'model', 'provider', 'deployment', 'tag'));

ALTER TABLE billing_budget_reservations
    ADD COLUMN IF NOT EXISTS organization_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_billing_budget_reservations_organization
    ON billing_budget_reservations(organization_id, created_at, state);
