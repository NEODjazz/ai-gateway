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
- timestamp in RFC 3339 UTC format

Pricing is configured through environment variables:

```text
BILLING_INPUT_PRICE_PER_1K=0
BILLING_OUTPUT_PRICE_PER_1K=0
BILLING_CURRENCY=USD
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

PostgreSQL is reserved for transactional financial data:

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
BILLING_LIMITS_ENABLED=false
BILLING_QUOTAS_ENABLED=false
BILLING_FINANCIAL_TRANSACTIONS_ENABLED=false
POSTGRES_DSN=
```

If one of these PostgreSQL-backed features is enabled without a configured policy store, the service rejects the billing request instead of silently skipping financial controls.

## Migrations

ClickHouse:

```text
migrations/clickhouse/001_usage_events.sql
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
```
