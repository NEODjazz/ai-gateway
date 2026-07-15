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

`POST /usage` returns the original `RequestContext` enriched with:

- `usage`
- `billing_event`
- billing metadata fields

`billing_event` includes:

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

PostgreSQL:

```text
migrations/postgres/001_financial_core.sql
```
