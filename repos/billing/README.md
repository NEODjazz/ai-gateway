# AI Gateway - Billing Service

Billing microservice for usage and token accounting.

## Run

```powershell
go run ./cmd/billing
```

## Endpoints

- `GET /healthz`
- `POST /usage`

## Collected data

`POST /usage` accepts a minimal usage event containing identity, an irreversible
credential fingerprint, provider/model metadata, phase, and token counters.
Prompt content, provider responses, bearer credentials, and anonymization values
are not sent to billing. The response contains only:

- `usage`
- billing metadata fields

The internally generated and persisted `billing_event` includes:

- request id
- user id
- team id
- roles
- API key fingerprint
- provider
- provider endpoint name/type
- model
- API type: `chat_completions` or `responses`
- status/error
- latency in ms
- estimated prompt tokens
- input/output/total tokens
- estimated cost
- currency
- catalog version and pricing key
- input/output price per one million tokens
- timestamp in RFC 3339 UTC format

Pricing is configured through the shared versioned `MODEL_CATALOG_JSON`. Prices
are expressed per one million tokens. Billing stores the matched catalog
version, pricing key, and rates in both the reservation and usage event, so a
catalog deployment cannot reprice an in-flight request. Use
`unknown_model_policy=deny` to reject models without an explicit price.

The old environment-wide price remains as a compatibility fallback when the
catalog policy is `legacy`:

```text
BILLING_INPUT_PRICE_PER_1K=0
BILLING_OUTPUT_PRICE_PER_1K=0
BILLING_CURRENCY=USD
MODEL_CATALOG_JSON={"version":"local-v1","unknown_model_policy":"legacy","models":[]}
```

## Storage model

Usage events are stored in ClickHouse when enabled:

```text
BILLING_USAGE_EVENTS_ENABLED=true
CLICKHOUSE_URL=http://clickhouse:8123
CLICKHOUSE_DATABASE=ai_gateway
CLICKHOUSE_USAGE_EVENTS_TABLE=usage_events
CLICKHOUSE_USERNAME=
CLICKHOUSE_PASSWORD=
```

PostgreSQL stores transactional billing state:

- users
- tariffs / price plans
- balances
- invoices
- payments
- limits
- quotas
- financial transactions

Feature flags:

```text
BILLING_TARIFFS_ENABLED=false
BILLING_LIMITS_ENABLED=true
BILLING_QUOTAS_ENABLED=true
BILLING_FINANCIAL_TRANSACTIONS_ENABLED=false
POSTGRES_DSN=
BILLING_RESERVATION_TTL_SECONDS=900
BILLING_DEFAULT_RESERVE_OUTPUT_TOKENS=1024
```

Limits and quotas are enforced atomically for `global`, `key`, `user`, `team`,
`model`, and `provider` scopes. A request first reserves its estimated input plus
maximum output allowance, then replaces the reservation with actual usage on
`commit` or releases it on `cancel`. Expired reservations stop consuming the
budget. Repeating the same `request_id` is idempotent; reusing it for another
billing identity or after the lifecycle is finalized returns HTTP 409. During
provider failover, an active reservation is atomically moved to the new
provider/model scope instead of being counted twice or bypassing that scope.

Policies are stored in `billing_budget_policies`. For example:

```sql
INSERT INTO billing_budget_policies
    (scope_type, scope_id, period, currency, max_cost, max_tokens)
VALUES
    ('team', 'team-42', 'month', 'USD', 100.00, 10000000);
```

When a matching limit is exhausted, billing returns HTTP 429 and the gateway
returns `budget_exceeded` without trying another provider. Missing PostgreSQL or
missing budget migrations make readiness and billing requests fail closed.
Tariffs and financial transactions remain unavailable and also fail closed when
their feature flags are enabled.

## Migrations

ClickHouse:

```text
migrations/clickhouse/001_usage_events.sql
migrations/clickhouse/002_usage_catalog.sql
```

Billing uses an explicit `reserve`, `commit`, and `cancel` lifecycle. Events are
identified by `request_id:phase`. With `BILLING_DURABLE_OUTBOX_ENABLED=true`,
the idempotency ledger and outbox are stored transactionally in PostgreSQL.
Workers claim events with `FOR UPDATE SKIP LOCKED`, recover stale claims, and
retry ClickHouse delivery with bounded backoff. When disabled, the service keeps
the process-local bounded outbox for development.

Delivery is at-least-once: a worker crash after ClickHouse accepts an insert but
before PostgreSQL records completion can produce a duplicate. Consumers should
deduplicate by `event_id` when exact accounting is required.

PostgreSQL:

```text
migrations/postgres/001_financial_core.sql
migrations/postgres/002_billing_outbox.sql
migrations/postgres/004_budgets.sql
migrations/postgres/005_pricing_snapshots.sql
```
