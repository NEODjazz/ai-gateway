# Production reliability

## Token accounting and request identity

TPM and remote billing reserve use the same context estimator. For chat, it includes messages, tool calls, tool schemas, tool choice and response format. For Responses, it includes input, instructions, tools, tool choice and text format. The estimate uses serialized context bytes (approximately four bytes per token); image input uses a fixed 4096-token estimate instead of charging for base64 length. This is a reservation estimate, not a provider tokenizer or a guarantee of exact multimodal usage. Provider-reported usage settles the final charge when available; fallback usage remains marked estimated.

`max_tokens` and `max_completion_tokens` produce identical chat reservations. The public chat API rejects supplying both. Responses uses `max_output_tokens`, with the existing `max_tokens` alias. Without an explicit output cap, reserve includes 1024 output tokens; this estimate does not impose a new upstream generation limit. Applications needing a bounded output reservation should send an explicit cap. Embeddings and rerank do not reserve output generation tokens.

Text completions reserve every prompt plus `max_tokens` for every server-generated candidate and prompt. Candidate count per prompt is the larger of `n` and `best_of`; the default output allowance is 16 tokens. Token-ID prompts use their exact input length, while text prompts use the bounded context estimate. Multiplication and addition saturate at the platform integer limit. The gateway rejects requests whose prompt count multiplied by `n` exceeds 128. Provider-reported usage settles the final charge, while a response without usage retains the full candidate reserve as explicitly estimated usage.

Native text-completion SSE is bounded to 32 MiB and each chunk is validated before forwarding. The gateway assembles choices and usage for post-response billing. Retry and cross-deployment fallback are allowed only before the first client write. A stream failure after that boundary produces a terminal error event and cannot start a second inference. If native streaming is unavailable, the existing bounded JSON call is replayed as buffered SSE after settlement.

Completion parameter compatibility is checked against the selected adapter before provider modules or billing. Ollama accepts only a single string prompt for this operation; string arrays and token-ID forms receive `unsupported_parameter` rather than being flattened or silently discarded.

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

## Provider and continuity rollout verification (2026-09-08)

Source revision `67531cb` was built and deployed to Rancher Desktop after its
Go 1.25.13 formatting, vet, unit/regression, race, and build checks passed.
The update includes native text embeddings, embedding response validation,
deployment capability intersection, Responses affinity/cache/fallback/shadow
fixes and observability, and synthetic Chat SSE for JSON-only deployments.

- Rancher Desktop reported Moby with Kubernetes enabled. Docker identified the
  `lima-rancher-desktop` server. The unchanged Dockerfile successfully built
  `ai-gateway-gateway:api-67531cb`; existing UI build layers were reused.
- Helm release `ai-gateway` revision 115 completed successfully. Comparing stored
  values with revision 114 confirmed that only `image.tag` changed.
- The gateway pod ran digest
  `sha256:53e95829b026e7eebf7b90e131987341225e288d8bcb4d0d3d04c2f7b7005db7`
  with zero restarts. All nine stack deployments reported their desired replica
  Ready.
- Local ingress returned 204 for `/healthz` and `/readyz`, 200 for `/ui/`, and
  `Cache-Control: no-store` for `/ui/app.css` and `/ui/app.js`.
- Chat, Responses, embeddings, Messages/count_tokens and native GenerateContent
  JSON/SSE/countTokens returned 401 without credentials. GenerateContent errors
  used the native `UNAUTHENTICATED` envelope. Messages additionally rejected a
  missing required version header with 400; requests with `anthropic-version:
  2023-06-01` reached the 401 authorization boundary.

These runtime smoke checks did not perform authenticated external inference or
browser visual QA. PostgreSQL integration and UI component tests were not rerun
for this rollout; their earlier execution is recorded above. No deployment
credentials, runtime settings, or database contents were changed.

## Responses accounting PostgreSQL verification (2026-09-08)

Source revision `d0ecfbb` passed `scripts/test-postgres-integration.sh` with Go
1.25.13 against PostgreSQL 16 in Rancher Desktop's dedicated test container.
The script applied migrations and ran `go test -race -count=1 ./...` for gateway,
auth and billing with `POSTGRES_INTEGRATION_REQUIRED=true`. All three modules
completed successfully; database tests were supplied their required DSNs.

Fresh databases were `verify_d0ecfbb_854728_gateway`,
`verify_d0ecfbb_854728_auth`, and `verify_d0ecfbb_854728_billing`, exposed only
through the test container's existing loopback port. Coverage includes atomic
budget reservations, joint budget/outbox rollback, durable and idempotent outbox,
worker restart/delivery failure, budget overflow, auth key persistence and control
plane storage. This complements the native Responses JSON/SSE regression tests;
it is not a live external-provider billing test.

The test container was returned to its original stopped state and its databases
were retained. Deployment databases and running stack resources were not changed.
The existing CI `postgres-integration` job invokes the same script with required
PostgreSQL testing, but GitHub Actions was not dispatched by this local check.
This verification did not update the deployed gateway image or rerun browser QA.

## Responses streaming reliability rollout (2026-09-08)

Source revision `f1fb3f1` was built and deployed after its Go 1.25.13 formatting,
vet, regression/unit, race and build checks passed. This update includes synthetic
Responses SSE, refusal and outcome preservation, bounded response parsing, exact
usage decoding, safe usage estimation, cache outcome filtering and required native
stream terminal events.

- Rancher Desktop reported Moby with Kubernetes enabled. The unchanged Dockerfile
  successfully built `ai-gateway-gateway:api-f1fb3f1`.
- Helm release `ai-gateway` revision 116 deployed successfully. Stored values
  differ from revision 115 only in `image.tag`.
- The gateway pod ran digest
  `sha256:75beb7cd8a9e9bc8a2adfbd096f3686e1c92b8653dff9435655d0c3fcd428b2f`
  with zero restarts. All nine deployments reported their desired replica Ready.
- Thirteen local ingress checks passed: health/readiness returned 204, UI and its
  assets returned 200, and CSS/JavaScript carried `Cache-Control: no-store`.
  Chat, Responses, embeddings, Messages/count_tokens and GenerateContent
  JSON/SSE/countTokens rejected unauthenticated requests with 401; GenerateContent
  preserved its native `UNAUTHENTICATED` error envelope.

Authenticated external inference and browser visual QA were not performed.
PostgreSQL integration was not repeated during rollout; the successful isolated
database verification at `d0ecfbb` is recorded above. UI code was unchanged and
its component suite was not rerun for this deployment.

## Responses continuation and tool history rollout (2026-09-08)

Source `31d49b6` was deployed after successful Go 1.25.13 formatting, vet,
unit/regression, race and build checks. It includes indexed Responses text,
function arguments and snapshots, terminal boundaries, reasoning summaries,
encrypted context preservation, include/store forwarding, protected protocol
identifiers and Anthropic tool-history conversion.

- Rancher Desktop reported Moby and enabled Kubernetes; Docker identified
  `lima-rancher-desktop`. The unchanged Dockerfile built
  `ai-gateway-gateway:api-31d49b6` successfully.
- Helm revision 117 deployed successfully. Comparison with revision 116 found
  only `image.tag` changed in stored values.
- The gateway ran digest
  `sha256:194ac152e28662b333904b3588fc7c9046898f39c82984aabd519abd69d6fd49`
  with zero restarts. All nine deployments reported their desired replicas Ready.
- Thirteen ingress checks passed: health/readiness 204, UI/assets 200, asset
  `no-store` headers and unauthenticated 401 responses for Chat, Responses,
  embeddings, Messages/count_tokens and GenerateContent JSON/SSE/countTokens.
  The Responses smoke payload included the new include/store fields.

Authenticated external inference and browser visual QA were not performed.
Stateless continuation was verified against local fake upstreams in repository
tests. PostgreSQL integration and UI component tests were not repeated for this
rollout; the earlier isolated PostgreSQL verification is recorded above.

### Responses diagnostics rollout (source 5d10695)

Source `5d10695`, previously verified with Go 1.25.13 vet, unit/regression,
race and build checks, was deployed to Rancher Desktop. It includes reasoning
request and usage details, annotations, provider error normalization, context
truncation, assistant phase preservation, and output token probabilities with
`top_logprobs` forwarding.

- Rancher Desktop reported Moby and enabled Kubernetes. Docker identified
  `lima-rancher-desktop`; the unchanged Dockerfile successfully built
  `ai-gateway-gateway:api-5d10695`.
- Helm revision 118 is deployed. Stored values comparison against revision 117
  confirmed that only `image.tag` changed.
- Gateway pod `ai-gateway-gateway-8f4f4fdbc-wfwph` was Ready with zero restarts,
  running digest
  `sha256:57bc929c1aa5fe7c08f7bff27e39b568826e083c9b7aa70327cd04ead55f2243`.
  All nine deployments had their desired Ready replica counts.
- Thirteen ingress checks passed: health/readiness 204, UI and assets 200 with
  asset `no-store`, and unauthenticated 401 responses for Chat, Responses,
  embeddings, Messages/count_tokens and GenerateContent JSON/SSE/countTokens.
  The Responses request exercised decoding of reasoning, truncation, store,
  include and explicit zero top_logprobs before authorization rejection.

These smoke checks do not prove authenticated provider execution. External
inference, browser visual QA, PostgreSQL integration and UI component tests were
not repeated for this rollout; provider behavior was checked by repository tests
against local fake upstreams before deployment.

### PostgreSQL verification after Responses option validation

Source `63dbfa9` passed `scripts/test-postgres-integration.sh` with Go 1.25.13
and PostgreSQL 16 in the dedicated Rancher Desktop test container. The script
applied migrations and ran `go test -race -count=1 ./...` for gateway, auth and
billing with all three test DSNs supplied and `POSTGRES_INTEGRATION_REQUIRED=true`.
All three modules completed successfully.

Fresh databases were `verify_63dbfa9_20260908_gateway`,
`verify_63dbfa9_20260908_auth` and `verify_63dbfa9_20260908_billing`.
The existing port binding was loopback-only (`127.0.0.1:15439`), checked before
starting the container. Coverage includes atomic budget reservations, joint
budget/outbox rollback, idempotent durable outbox, worker restart/delivery failure,
budget overflow, auth key lifecycle and control-plane persistence.

The dedicated container was returned to its original stopped state; test databases
were retained. Deployment databases were not used. The CI postgres-integration job
still invokes the same required-test script; this run did not dispatch GitHub
Actions. This is database integration evidence, not external-provider execution
or browser QA. The deployed image remains source `5d10695` at Helm revision 118.
