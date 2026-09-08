# Production reliability

## Token accounting and request identity

TPM and remote billing reserve use the same context estimator. For chat, it includes messages, tool calls, tool schemas, tool choice and response format. For Responses, it includes input, instructions, tools, tool choice and text format. The estimate uses serialized context bytes (approximately four bytes per token); image input uses a fixed 4096-token estimate instead of charging for base64 length. This is a reservation estimate, not a provider tokenizer or a guarantee of exact multimodal usage. Provider-reported usage settles the final charge when available; fallback usage remains marked estimated.

`max_tokens` and `max_completion_tokens` produce identical chat reservations. The public chat API rejects supplying both. Responses uses `max_output_tokens`, with the existing `max_tokens` alias. Without an explicit output cap, reserve includes 1024 output tokens; this estimate does not impose a new upstream generation limit. Applications needing a bounded output reservation should send an explicit cap. Embeddings and rerank do not reserve output generation tokens.

The external `X-Request-ID` remains a correlation identifier. Every inference execution receives a server-generated `X-Execution-ID`; client-supplied execution IDs are ignored. Billing lifecycle `request_id` and request-log IDs now identify this execution. Reusing an external correlation ID never makes two inference calls a single billing operation. HTTP logs contain both `request_id` and `execution_id`; traces expose `ai.request.id` and `ai.execution.id`. This intentionally changes billing/request-log ID semantics while preserving payload field names.

## Cache isolation and memory limits

Exact and semantic response cache scopes include credential ID, user, team, organization, sorted roles/tags/model/tool grants, evaluated access-group grants and effective policy metadata (`policy.*`, `provider.modules.*`, `provider.guardrail.*`). Scope data is hashed. Requests without a credential do not use the response cache. There is no implicit sharing between members of a team. Changes to effective policy cause cache misses; old entries expire normally. Exact cache uses a new key namespace. Responses affinity is isolated by credential and user.

Global limits apply per in-memory store instance, shared across its request scopes:

| Store | Entry limit | Data limit | Reclamation |
| --- | ---: | ---: | --- |
| Exact response cache | 1024 | 64 MiB including keys | FIFO eviction and configured TTL |
| Semantic response cache | configured, capped at 1024 (default 100) | 64 MiB including scope, vectors and payload | oldest-expiry eviction and configured TTL |
| Responses affinity | 4096 | 2 MiB including keys | FIFO eviction and configured TTL |
| Memory rate-limit windows | 10000 | fixed-size SHA-256 identity keys and counters | fixed one-minute TTL; reject new identities at capacity |
| Billing lifecycle dedup | 10000 | fixed-size hashed keys and metadata | 15-minute TTL; reject new claims at capacity |

Per-response size limits still apply. Store metadata and allocations add overhead to these data limits, but entry counts are bounded. Expired items are reclaimed on access/insertion; no cleanup goroutine is needed. Redis-backed caches retain their existing TTL behavior and require Redis capacity management. In-memory lifecycle dedup is only a short-lived best-effort mode: after expiry or process restart, old events may be accepted again. Durable billing uses PostgreSQL instead.

## Rate-limit counter guarantees

Memory and Redis rate limiting reject requests that exceed the remaining TPM
allowance without overflowing integer addition. Redis uses exact decimal string
arithmetic rather than Lua floating-point counter arithmetic, including above
2^53. Counters for an unlimited dimension saturate at the platform's maximum
integer; tightening that limit within the window cannot erase prior usage.
Negative token estimates are clamped to zero in both stores and cannot refund
usage. Rejected requests do not increment either counter.

The memory store hashes identity keys to a fixed 32-byte value and holds at most
10,000 active windows per instance. Expired windows are reclaimed on the next
limited request, including after idle periods. Existing identities retain their
original expiry and remain usable at capacity; a new identity receives HTTP 503
`rate_limit_unavailable` when all slots are active. No active window is evicted.
Normal quota exhaustion remains HTTP 429 with `Retry-After`. The capacity is an
internal fixed bound, not a new configuration option. These limits remain
per-process in memory mode; use the Redis store for shared multi-instance quotas.

## Billing delivery and failure behavior

Required billing with usage-event reporting enabled requires `BILLING_DURABLE_OUTBOX_ENABLED=true` and a working PostgreSQL database. Memory-only usage delivery makes readiness fail in this configuration. Deployments that previously enabled required usage reporting without a durable outbox must configure PostgreSQL and enable the durable path before rollout. Existing migrations remain sufficient.

With PostgreSQL budget policy and durable outbox, budget state, lifecycle ledger and delivery intent are committed in one database transaction. A failed outbox insert rolls back budget changes. Replays do not create another delivery event. Pending events survive process restart and failed delivery remains retryable. Delivery to the external usage sink is at-least-once: the sink must retain its existing event-ID deduplication behavior for ambiguous network outcomes.

The optional memory outbox has a bounded queue, bounded retries and cancellable delivery. Shutdown drains the queue within a deadline; exhausted retries and forced shutdown losses are logged without event content. `/metrics` exposes `billing_memory_outbox_accepted_total`, `delivered_total`, `failed_total`, `dropped_total`, `rejected_total` (each with the same `billing_memory_outbox_` prefix) and `billing_memory_outbox_queued`. Alert on increases in failed/dropped/rejected counters. An abrupt process kill can still lose memory-only events without emitting a final metric, so memory delivery is unsuitable for durable accounting.

## UI behavior

Overview uses backend totals where available and loads each inventory card independently, with per-card error and retry. Usage requests and drilldowns cancel obsolete loads and ignore their late success/error results. Modal forms use the existing UI library's focus containment, Escape handling and focus restoration; saving forms can disable dismissal. CSV cells escape quotes, commas and newlines, use CRLF row endings, and neutralize spreadsheet formula prefixes in text fields. CSS without a content hash is served with `no-store`; only versioned assets receive immutable caching.

## Required PostgreSQL verification

The `PostgreSQL integration` CI job starts PostgreSQL 16, creates separate databases for gateway, auth and billing, applies migrations and runs all three services' tests with the race detector. Missing DSNs are failures when `POSTGRES_INTEGRATION_REQUIRED=true`, preventing silent skips in that job.

For local execution, provision **disposable test databases** and set `CONTROL_PLANE_POSTGRES_TEST_DSN`, `AUTH_POSTGRES_TEST_DSN`, and `BILLING_POSTGRES_TEST_DSN`, then run `bash scripts/test-postgres-integration.sh`. The script applies migrations and integration tests modify database contents. Scenarios include control-plane persistence, key lookup, budget reservation/conflicts, durable replay, atomic budget/outbox rollback, recovery after failed delivery and worker restart. Repository branch protection must select this job as a required status check if merge blocking is desired; workflow changes alone do not configure branch protection.

## Verification of this change (2026-09-08)

- Go 1.25.13: `gofmt`, `go vet ./...`, `go test ./...` and `go build ./...` completed successfully for gateway, auth, billing, anonymizer, dlp and av.
- The PostgreSQL integration script completed successfully against PostgreSQL 16 using three isolated databases and `go test -race -count=1 ./...`. The overflow regression also verifies that an extreme output reservation cannot wrap around the existing token budget.
- UI: all 143 component tests across 42 files passed; `npm run build` completed TypeScript checking and regenerated embedded assets. Vite still reports the existing large-chunk advisory.
- UI interactions were verified with component tests, including focus cycling, Escape/return focus and stale asynchronous requests. Authenticated browser visual QA was not performed; an earlier automatic approval review rejected use of the demo login token. No production credentials or external inference services were used.
- GitHub Actions itself was not dispatched from this workspace; the same PostgreSQL script was exercised locally. No dependencies, Go version, deployment credentials or production settings were changed.

## Local rollout verification (2026-09-08)

Rancher Desktop releases `ai-gateway` (revision 113) and `ai-gateway-billing` (revision 24) were upgraded using their existing Helm values and locally built `reliability-20260908` image tags. All nine stack components became Ready; the updated gateway and billing pods had no restarts. Gateway emitted one transient connection-refused readiness event while starting, then passed readiness checks.

The existing ingress served `/healthz` and `/readyz` with HTTP 204 and `/ui/` with HTTP 200. Served UI bundles matched the workspace build byte-for-byte. Unversioned CSS/JS returned `no-store`; hashed page bundles retained immutable caching. Unauthenticated inference and administrative requests returned 401. Billing exposed its memory-outbox metrics, and the durable outbox had zero pending events and zero pending retries. These deployment smoke checks did not perform authenticated inference against an external provider.

## Rate-limit and inference validation (2026-09-08)

The counter, cardinality and strict inference-decoding regressions are permanent
repository tests. They cover an oversized reservation after prior consumption,
exact `MaxInt` admission boundaries, unchanged counters after rejection,
saturation and negative estimates, concurrent admission, capacity exhaustion,
HTTP 503 mapping, reclamation after expiry/idle periods, rejection of unsupported
parameters on all four inference endpoints, and preservation of arbitrary tool
and output-schema properties.

Go 1.25.13 `gofmt`, `go vet ./...`, `go test -count=1 ./...`,
`go test -race -count=1 ./...` and `go build ./...` passed for the gateway module.
The rate-limit contract and Redis rate-limit tests also passed with the race
detector against an isolated Redis 7 container in Rancher Desktop. Its fixed TTL,
atomic admission and large request/token counters were exercised; the test
container was stopped afterwards. The default tests use miniredis; setting
`REDIS_TEST_ADDR` runs the counter contract and Redis store integration fixtures
against a disposable Redis server. Do not point this variable at deployment data.

This validation did not rerun PostgreSQL integration or UI tests because these
fixes do not change database or UI code. The earlier rollout above predates these
additional rate-limit and inference changes.

## Native API rollout verification (2026-09-08)

Source revision `089b625` was validated and deployed to Rancher Desktop:

- `scripts/test-postgres-integration.sh` passed with Go 1.25.13, required
  PostgreSQL tests enabled, and `go test -race -count=1 ./...` for gateway, auth
  and billing. PostgreSQL 16 ran in the separate local test container, using
  three new databases (`api_089b625_gateway`, `api_089b625_auth`,
  `api_089b625_billing`). The test container was stopped afterwards; deployment
  databases were not used for these tests.
- All 144 UI component tests across 42 files passed. The existing Dockerfile
  built the UI and gateway successfully, producing image
  `ai-gateway-gateway:api-089b625` with digest
  `sha256:6bf0182ed7bc6e482e2aacd6f4dcd8956ea0f44e8c1fec94c51a55e6925c0029`.
  The build retained the existing Vite large-chunk advisory.
- Helm release `ai-gateway` revision 114 reused its existing values with only
  the image tag override. All nine stack deployments reported Ready. The new
  gateway pod ran the expected image digest with zero restarts.
- Local ingress checks returned 204 for health/readiness and 200 for the UI.
  Unversioned `app.css` and `app.js` returned `Cache-Control: no-store`.
  GenerateContent JSON/SSE/countTokens and Messages/count_tokens returned 401
  without credentials; GenerateContent errors used the native UNAUTHENTICATED
  envelope.

These runtime smoke checks did not execute authenticated external inference or
perform authenticated browser visual QA. Native generation, policy and billing
behavior was exercised by the repository regression tests and local fake upstream
servers. GitHub Actions itself was not dispatched; its PostgreSQL script was run
locally against a real isolated database server.
