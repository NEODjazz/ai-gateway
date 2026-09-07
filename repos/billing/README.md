# AI Gateway - Billing Service

Billing microservice for usage and token accounting.

## Run

```powershell
go run ./cmd/billing
```

## Endpoints

- `GET /healthz`
- `GET /livez`
- `POST /usage`
- `/internal/v1/*` management endpoints for budgets, reports, request logs, and
  audit events; these require `BILLING_MANAGEMENT_SHARED_SECRET` when configured

`/livez` reports process liveness. `/healthz` is the readiness check: it verifies
the enabled billing dependencies and the audit PostgreSQL store. There is no
separate `/readyz` endpoint.

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
- API type: `chat_completions`, `responses`, `embeddings`, or `rerank`
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

Limits and quotas are enforced atomically for `global`, `organization`, `key`,
`user`, `team`, `model`, `provider`, and credential `tag` scopes. A request with multiple tags
consumes every matching tag budget. Tags are snapshotted in the reservation so
retries, fallback, commit, and summary use the same billing identity. A request
first reserves its estimated input plus maximum output allowance, then replaces
the reservation with actual usage on
`commit` or releases it on `cancel`. Expired reservations stop consuming the
budget. Repeating the same `request_id` is idempotent; reusing it for another
billing identity or after the lifecycle is finalized returns HTTP 409. During
provider failover, an active reservation is atomically moved to the new
provider/model scope instead of being counted twice or bypassing that scope.
The organization is snapshotted as well, so changing key ownership while a
request is in flight cannot move its reservation to another budget.

Policies are stored in `billing_budget_policies`. For example:

```sql
INSERT INTO billing_budget_policies
    (scope_type, scope_id, period, currency, max_cost, max_tokens)
VALUES
    ('team', 'team-42', 'month', 'USD', 100.00, 10000000);
```

Operators can manage the same policies through the gateway admin API instead
of editing PostgreSQL directly. Billing exposes only its cluster-internal
counterpart and protects it with a dedicated secret:

```text
BILLING_MANAGEMENT_SHARED_SECRET=<independent internal secret>
GET|POST  /internal/v1/budgets
GET        /internal/v1/budgets?expand=summaries
GET|PUT|DELETE /internal/v1/budgets/{id}
GET /internal/v1/budgets/{id}/summary
GET /internal/v1/usage/report?days=30
```

Usage events and reports retain provider-reported `cache_read_input_tokens` and
`cache_write_input_tokens`. Apply ClickHouse migration
`008_usage_cache_tokens.sql` before deploying a billing binary that writes or
queries these fields.

`DELETE` is a soft disable. Summary includes committed usage and unexpired
reservations, using the same period, scope, and currency calculation as
enforcement. `expand=summaries` calculates every policy in one PostgreSQL query
and returns a map keyed by policy ID; reservations in another currency never
contribute to that policy's cost or token utilization.
The usage report accepts a bounded 1–90 day range and aggregates final
`commit`/`cancel` outcomes by currency, day, canonical public model, and managed
provider and virtual-key tag. Untagged events appear as `Untagged`; a request
with multiple tags is intentionally attributed to every tag. The upstream response model and deployment endpoint remain separate
request-log dimensions and do not split usage totals.
Currencies are never combined into a single spend total.
The secret must not be shared with virtual-key management or client Bearer
credentials.

The `/usage` lifecycle endpoint can independently require
`BILLING_SHARED_SECRET` via `X-Service-Token`. Configure the same value on the
gateway; it authenticates pricing snapshots and must differ from the budget
management secret. An empty value is supported only for local compatibility.

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
migrations/clickhouse/003_usage_identity.sql
migrations/clickhouse/004_usage_sessions.sql
migrations/clickhouse/005_usage_observability.sql
migrations/clickhouse/006_usage_traces.sql
migrations/clickhouse/007_usage_tags.sql
migrations/clickhouse/008_usage_cache_tokens.sql
```

Final usage events retain normalized provider/deployment identity, organization,
session and OpenTelemetry trace scope, end-to-end latency and streaming TTFT, retry/fallback counts,
cache status/type, and whether token usage came from the provider or the bounded
gateway estimator. Estimated usage is explicitly marked and must not be treated
as exact provider metering.

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
migrations/postgres/006_management_audit.sql
migrations/postgres/007_tag_budgets.sql
migrations/postgres/008_organization_budgets.sql
```

`management_audit_events` is an append-only management journal queried through
the gateway admin API. The internal append/list contract is protected by
`BILLING_MANAGEMENT_SHARED_SECRET`; audit identity comes from authenticated
gateway headers rather than the JSON body.
