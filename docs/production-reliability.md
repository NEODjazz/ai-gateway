# Production reliability

## Token accounting and request identity

TPM and remote billing reserve use the same context estimator. For chat, it includes messages, tool calls, tool schemas, tool choice and response format. For Responses, it includes input, instructions, tools, tool choice and text format. The estimate uses serialized context bytes (approximately four bytes per token); image input uses a fixed 4096-token estimate instead of charging for base64 length. This is a reservation estimate, not a provider tokenizer or a guarantee of exact multimodal usage. Provider-reported usage settles the final charge when available; fallback usage remains marked estimated.

Native Messages and GenerateContent requests retain their API family in billing
reserve, commit and failure events. The gateway sets this classification before
the policy pipeline runs; arbitrary client request metadata cannot override it.
Messages `metadata.user_id` is bounded to 512 Unicode characters and remains
separate from the authenticated credential, user, organization, and execution
identities used for policy and billing. Provider-reported thinking-token details
are accepted only when nonnegative and no greater than total output tokens.
Compatible Responses also preserves nonnegative provider-reported cached output
token details for JSON and terminal SSE usage without adding them to aggregate
output or total tokens. Provider-reported Responses input reasoning details follow
the same rule and remain separate from aggregate input and total tokens.

Explicit prompt-cache breakpoints are limited to four across Chat messages and
tools. They are part of token reservation and exact cache identity. Semantic
response caching is disabled for these requests. Native prompt caching also
requires the deployment and model to declare `prompt_cache`; provider cache-read
and cache-creation token counters remain distinct during billing settlement.

`max_tokens` and `max_completion_tokens` produce identical chat reservations. The public chat API rejects supplying both. Responses uses `max_output_tokens`, with the existing `max_tokens` alias. Without an explicit output cap, reserve includes 1024 output tokens; this estimate does not impose a new upstream generation limit. Applications needing a bounded output reservation should send an explicit cap. Embeddings and rerank do not reserve output generation tokens.

Text completions reserve every prompt plus `max_tokens` for every server-generated candidate and prompt. Candidate count per prompt is the larger of `n` and `best_of`; the default output allowance is 16 tokens. Token-ID prompts use their exact input length, while text prompts use the bounded context estimate. Multiplication and addition saturate at the platform integer limit. The gateway rejects requests whose prompt count multiplied by `n` exceeds 128. Provider-reported usage settles the final charge, while a response without usage retains the full candidate reserve as explicitly estimated usage.

Native text-completion SSE is bounded to 32 MiB and each chunk is validated before forwarding. The gateway assembles choices and usage for post-response billing. Retry and cross-deployment fallback are allowed only before the first client write. A stream failure after that boundary produces a terminal error event and cannot start a second inference. If native streaming is unavailable, the existing bounded JSON call is replayed as buffered SSE after settlement.

Completion parameter compatibility is checked against the selected adapter before provider modules or billing. Ollama accepts only a single string prompt for this operation; string arrays and token-ID forms receive `unsupported_parameter` rather than being flattened or silently discarded.
Ollama completion streams explicitly request a final usage chunk through its provider-specific stream option. Generic compatible deployments do not receive this extra field. If a provider still omits usage, settlement retains the full candidate reserve and marks it estimated.

The external `X-Request-ID` remains a correlation identifier. Every inference execution receives a server-generated `X-Execution-ID`; client-supplied execution IDs are ignored. Billing lifecycle `request_id` and request-log IDs now identify this execution. Reusing an external correlation ID never makes two inference calls a single billing operation. HTTP logs contain both `request_id` and `execution_id`; traces expose `ai.request.id` and `ai.execution.id`. This intentionally changes billing/request-log ID semantics while preserving payload field names.

## Cache isolation and memory limits

Exact and semantic response cache scopes include credential ID, user, team, organization, sorted roles/tags/model/tool grants, evaluated access-group grants and effective policy metadata (`policy.*`, `provider.modules.*`, `provider.guardrail.*`). Scope data is hashed. Requests without a credential do not use the response cache. There is no implicit sharing between members of a team. Changes to effective policy cause cache misses; old entries expire normally. Exact cache uses a new key namespace. Responses affinity is isolated by credential and user.

Native provider request metadata is validated and included in the input DLP
projection. Because it annotates an actual provider invocation, requests carrying
it bypass exact and semantic response caches so that accepted calls always reach
the provider log.

Opaque native model inference fields are limited to 64 KiB, validated as non-null
JSON and included in the input DLP projection. They participate in exact-cache
identity and bypass semantic cache because their effect cannot be normalized by
the gateway.

Native provider guardrail requests bypass both response caches so every accepted
call is evaluated upstream. Guardrail identifiers, versions and trace modes are
validated before routing. Returned trace data is accepted only when tracing was
explicitly enabled, remains subject to the bounded response and output DLP paths,
and is preserved in the native response.

Native Converse streaming reuses the gateway Chat streaming lifecycle. It emits
bounded AWS EventStream messages with prelude and message CRC validation by
contract, permits retries and deployment fallback only before the first event,
and closes successful streams with `messageStop` followed by provider-reported
usage metadata. Requests rejected before the first event retain the regular JSON
error envelope and status.

The Bedrock adapter reads upstream EventStream incrementally with 16 MiB per
message and 64 MiB per-stream limits. Prelude CRC, message CRC, header framing,
event order, content unions, tool identifiers and final provider usage are
validated before settlement. Provider exceptions retain retryable rate-limit and
availability classes; retries and fallback stop once an output delta is exposed.
Provider-reported cache-read and cache-write input tokens are included in prompt
and total usage before quota settlement and billing, while the native Converse
response preserves the provider's split counters.
Explicit Bedrock prompt-cache checkpoints require the deployment's
`prompt_cache` capability. The gateway accepts at most four checkpoints, maps
only validated system, message and tool boundaries, and includes checkpoint TTL
and placement in exact-cache identity. Native cache points allow the provider's
default five-minute TTL or an explicit five-minute or one-hour TTL.

A2A plain-text, Markdown and CSV documents are decoded only from strict bounded
base64 or an authorized bounded HTTPS fetch. Invalid UTF-8 and NUL bytes are
rejected before inference. Accepted document text enters the shared Responses
input policy, token reserve and billing path, and remote URLs are replaced with
validated inline content before task persistence.

A2A MP4 and WebM video parts use the shared signature-validated video contract.
Inline and authorized HTTPS inputs have independent count and byte limits, enter
the AV, model-capability, TPM and billing pipeline, and are persisted only after
remote URLs have been replaced with validated inline bytes. Remote fetches do not
forward client authorization headers.

Rancher Desktop built `ai-gateway-gateway:gaps-70dd297` with image ID
`sha256:ff06c3eb35d717bdc040fdc24343716474a6472191aa257c68d33b04af27a764`.
Helm revision 433 completed successfully, and pod
`ai-gateway-gateway-9f4c5977d-9bgqg` became Ready with zero restarts. Its live
endpoint served OpenAPI 0.1.333. No agent profile was configured in the deployed
control plane, so the Agent Card and SendMessage runtime checks remained covered
by the end-to-end HTTP regressions without mutating control-plane data.

Synchronous vector-store search resolves ownership before reading files and
allows only bounded UTF-8 text formats with `purpose=assistants`. Query and file
chunks pass through the configured inference content policy and token admission
before one billed embeddings batch. Malformed, non-finite or dimensionally
inconsistent vectors fail closed before ranking.

Chat cache identity also includes validated provider-native message blocks, their
reserved input size, Bedrock service tier, latency selection and requested
additional response-field paths. Semantic
cache rejects opaque native message blocks because their meaning cannot be
represented by the text embedding. Provider-native controls remain part of the
semantic settings fingerprint. This prevents requests with different documents
or execution controls from sharing a cached result.

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

Deployment `rate_limit_rpm` and `rate_limit_tpm` use the same atomic fixed-window
contract. TPM reserves the full effective input plus bounded output before an
upstream call, including tool schemas and either supported output-token field.
Quota exhaustion skips the limited deployment and remains eligible for routing
fallback. Cache hits do not consume deployment quota. Shadow calls and response
lifecycle operations consume the quota of the deployment they contact. The
memory implementation fails closed after 10,000 active deployment identities;
Redis shares counters across gateway replicas. Exhaustion returns HTTP 429 with
`deployment_rate_limit_exceeded` and the fixed window's remaining `Retry-After`.
Managed providers can also set shared `rate_limit_rpm` and `rate_limit_tpm`
across all of their deployments. Provider and deployment reservations are one
atomic operation: if either scope is exhausted, neither counter is incremented.
Provider exhaustion returns `provider_rate_limit_exceeded` with `Retry-After`.

## Billing delivery and failure behavior

Required billing with usage-event reporting enabled requires `BILLING_DURABLE_OUTBOX_ENABLED=true` and a working PostgreSQL database. Memory-only usage delivery makes readiness fail in this configuration. Deployments that previously enabled required usage reporting without a durable outbox must configure PostgreSQL and enable the durable path before rollout. Existing migrations remain sufficient.

With PostgreSQL budget policy and durable outbox, budget state, lifecycle ledger and delivery intent are committed in one database transaction. A failed outbox insert rolls back budget changes. Replays do not create another delivery event. Pending events survive process restart and failed delivery remains retryable. Delivery to the external usage sink is at-least-once: the sink must retain its existing event-ID deduplication behavior for ambiguous network outcomes.

The optional memory outbox has a bounded queue, bounded retries and cancellable delivery. Shutdown drains the queue within a deadline; exhausted retries and forced shutdown losses are logged without event content. `/metrics` exposes `billing_memory_outbox_accepted_total`, `delivered_total`, `failed_total`, `dropped_total`, `rejected_total` (each with the same `billing_memory_outbox_` prefix) and `billing_memory_outbox_queued`. Alert on increases in failed/dropped/rejected counters. An abrupt process kill can still lose memory-only events without emitting a final metric, so memory delivery is unsuitable for durable accounting.

Provider inference, token-count and model-discovery clients reject HTTP redirects. A configured endpoint therefore cannot move an authenticated request to another location; the original 3xx is handled as a provider failure.

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

## AWS web identity rollout (2026-09-10)

Source `188030f` adds projected web-identity credentials to the Bedrock SigV4
credential chain. The gateway exchanges `AWS_ROLE_ARN` and the bounded token file
from `AWS_WEB_IDENTITY_TOKEN_FILE` through the regional STS endpoint, caches the
temporary credential, coalesces refreshes and stops using it at expiration.
Malformed configuration and responses fail closed before Bedrock execution.

- Go formatting, vet, all unit/regression tests, race tests and build passed.
- Protocol tests used a local STS server and covered the exact form request,
  caching, regional endpoints, incomplete configuration, unsafe token paths,
  size limits, malformed XML, non-success status and expired credentials.
- Rancher Desktop built `ai-gateway-gateway:api-188030f`; Helm revision 277
  completed successfully with one Ready gateway pod and zero restarts.
- Ingress checks returned 204 for health/readiness and 200 for the UI and admin
  SPA route. The served OpenAPI contract reported version 0.1.174 and the
  projected web-identity credential chain.

No live cloud role was assumed and no paid provider inference was performed.
The protocol and signing paths are covered by isolated tests; cloud IAM policy,
trust relationship and provider availability remain deployment responsibilities.

## Chat, SCIM and parameter contract rollout (2026-09-12)

Source `dd16398` includes SCIM base discovery and server-side sorting, additive
user-role PATCH semantics, optional Group attribute removal, and the `default`
Chat reasoning effort with complete adapter-specific `ChatGenerationOptions`
capability reporting. Native Cohere now rejects `web_fetch_options` instead of
discarding it. Demo now rejects unsupported Responses parameters instead of
discarding them, and the provider-capability endpoint exposes validated
Responses option, reasoning-effort and service-tier matrices for every managed
provider type. Each behavior change passed its focused regressions and a full
gateway `go test -race ./...`; gateway vet and build also passed.

- Rancher Desktop reported Moby 29.1.3 with Kubernetes 1.36.3 enabled. The
  unchanged Dockerfile built `ai-gateway-gateway:gaps-dd16398` successfully.
- Helm release `ai-gateway` revision 399 completed successfully using the
  existing stored values with only `image.tag` overridden.
- Gateway pod `ai-gateway-gateway-6b8568565b-swsgg` became Ready with zero
  restarts and ran `ai-gateway-gateway:gaps-dd16398`.
- In-pod loopback checks returned 204 for readiness and 200 with
  `application/scim+json` for `/scim/v2`; discovery returned both User and Group
  resource types. A Chat request using `reasoning_effort=default` passed request
  validation and reached routing/provider execution.
- The live provider-capability endpoint advertised `default` for OpenAI, Azure
  OpenAI, Groq and OpenRouter adapters while preserving the documented native
  Mistral range through `xhigh`. It also returned exact supported-option lists
  for all managed Chat adapters; Cohere's list excluded `web_fetch_options`.
- The live capability endpoint returned an empty Responses option profile for
  Demo and adapter-specific profiles for OpenAI, Anthropic, Groq and xAI. The
  served OpenAPI contract reported version 0.1.301.

The host ingress did not accept a connection on port 80 during this rollout, so
application checks used the pod loopback endpoint. The Chat smoke request used a
nonexistent model and did not perform paid inference. SCIM mutations were covered
by HTTP regression tests and were not repeated against deployment data.

### Embeddings and Rerank validator rollout

Source `70a73d1` rejects the unsupported Demo Embeddings `user` parameter and
exposes the existing compatible, OpenRouter, Cohere and Voyage Rerank parameter
checks as side-effect-free adapter validators. Focused protocol and race tests,
followed by the full gateway race suite, vet and build, passed.

Rancher Desktop built `ai-gateway-gateway:gaps-70a73d1`. Helm revision 400
completed successfully with the stored release values, and pod
`ai-gateway-gateway-5bb6f69d6b-bzhwd` became Ready with zero restarts on that
image.

Source `2e5d04c` adds validator-derived Embeddings and Rerank capability
profiles. Rancher Desktop built `ai-gateway-gateway:gaps-2e5d04c`; Helm revision
401 completed successfully, and pod `ai-gateway-gateway-6f8f5dd8-9skzf` became
Ready with zero restarts. The live admin endpoint returned the expected distinct
input and option profiles for Demo, OpenRouter, Cohere and Voyage.

Source `d71eade` makes legacy Completions an explicit managed operation and
publishes validator-derived prompt forms and controls. Rancher Desktop built
`ai-gateway-gateway:gaps-d71eade`; Helm revision 402 completed successfully, and
pod `ai-gateway-gateway-59c4dbcf88-4zpqm` became Ready with zero restarts. The
live endpoint confirmed string-only Ollama and Mistral profiles and all four
prompt forms for the OpenAI adapter.

Source `15e7e0f` adds Moderation input and option profiles. Rancher Desktop built
`ai-gateway-gateway:gaps-15e7e0f`; Helm revision 403 completed successfully, and
pod `ai-gateway-gateway-587b4d9bb-gj2dm` became Ready with zero restarts. The
live endpoint confirmed compatible text, text-array and content-part input and
the native Mistral text-only profile with metadata support.

Source `3d6b722` adds validator-derived Search query and option profiles.
Rancher Desktop built `ai-gateway-gateway:gaps-3d6b722`; Helm revision 404
completed successfully, and pod `ai-gateway-gateway-7c99b7479f-f4f78` became
Ready with zero restarts. The live endpoint confirmed scalar/batch queries and
all four bounded Search controls for OpenAI, Azure and compatible adapters.

Source `45c98a9` adds execution-validator-derived Image Generation profiles.
Rancher Desktop built `ai-gateway-gateway:gaps-45c98a9`; Helm revision 405
completed successfully, and pod `ai-gateway-gateway-56c5fb65bb-l49vp` became
Ready with zero restarts. The live endpoint confirmed distinct full compatible,
OpenRouter, Gemini and xAI generation-control sets.

Source `2a8e50a` adds execution-validator-derived Image Edit profiles, including
mask support and the maximum accepted image count. Rancher Desktop built
`ai-gateway-gateway:gaps-2a8e50a`; Helm revision 406 completed successfully, and
pod `ai-gateway-gateway-7d66b5b84f-wsm2x` became Ready with zero restarts. The
live endpoint confirmed full compatible/Azure controls with masks and eight
images, OpenRouter's reduced eight-image set, Gemini's `n` and
`response_format` controls with eight images, and xAI's three controls with a
five-image limit.

Source `3d0a599` adds execution-validator-derived Image Variation profiles.
Rancher Desktop built `ai-gateway-gateway:gaps-3d0a599`; Helm revision 407
completed successfully, and pod `ai-gateway-gateway-654f9bb784-5sp2z` became
Ready with zero restarts. The live endpoint confirmed `n`, `response_format`,
`size` and `user` for compatible/Azure adapters and the reduced `n` plus
`response_format` set for Gemini.

Source `20af6e7` adds execution-validator-derived Audio Transcription profiles
for all eight implementing adapter types while retaining duration reserve in the
execution path. Rancher Desktop built `ai-gateway-gateway:gaps-20af6e7`; Helm
revision 408 completed successfully, and pod
`ai-gateway-gateway-7d894dcd4-fv42m` became Ready with zero restarts. The live
endpoint returned the exact regression-locked option sets for compatible/Azure,
OpenRouter, Gemini, Mistral, Groq and xAI transports.

Source `88e7732` adds execution-validator-derived Audio Translation profiles.
Rancher Desktop built `ai-gateway-gateway:gaps-88e7732`; Helm revision 409
completed successfully, and pod `ai-gateway-gateway-78c4d5dfc9-cw242` became
Ready with zero restarts. The live endpoint confirmed the compatible/Azure and
Mistral option set plus the native Gemini and Groq language-aware sets.

Source `fd6cf98` adds execution-validator-derived Text-to-Speech profiles for
all eight implementing adapter types. Rancher Desktop built
`ai-gateway-gateway:gaps-fd6cf98`; Helm revision 410 completed successfully, and
pod `ai-gateway-gateway-774bf574ff-b4mxz` became Ready with zero restarts. The
live endpoint returned the exact regression-locked control sets for compatible,
Azure, OpenRouter, Gemini, Mistral, Groq and xAI transports.

Source `9323119` adds execution-validator-derived OCR profiles with document
form isolation. Rancher Desktop built `ai-gateway-gateway:gaps-9323119`; Helm
revision 411 completed successfully, and pod
`ai-gateway-gateway-585bbd56cf-f97sn` became Ready with zero restarts. The live
endpoint confirmed Mistral HTTPS/inline PDF and image inputs with all extraction
controls, and Gemini inline-only PDF/image inputs with page selection and
Markdown table output.

Source `73af8a6` adds execution-validator-derived Video creation profiles.
Rancher Desktop built `ai-gateway-gateway:gaps-73af8a6`; Helm revision 412
completed successfully, and pod `ai-gateway-gateway-b5b784c67-q7rfd` became
Ready with zero restarts. The live endpoint confirmed duration, size and input
reference controls for compatible/OpenAI and native xAI video creation.

Source `d0e9f77` adds fail-fast adapter validation and capability profiles for
Fine-tuning creation. Rancher Desktop built
`ai-gateway-gateway:gaps-d0e9f77`; Helm revision 413 completed successfully, and
pod `ai-gateway-gateway-5cc4bf6857-wbknk` became Ready with zero restarts. The
live endpoint confirmed validation-file, suffix, seed, metadata and method
controls for OpenAI and compatible adapters.

Source `7b7dfea` adds Container creation profiles from the adapter validator and
effective `container_files` capability. Rancher Desktop built
`ai-gateway-gateway:gaps-7b7dfea`; Helm revision 414 completed successfully, and
pod `ai-gateway-gateway-66b64cbfb5-lmw4x` became Ready with zero restarts. The
live endpoint confirmed expiration, memory, network-policy and initial-file
controls for OpenAI and compatible adapters.

Source `dd59d54` adds validator-derived values to Video creation and extension
profiles. Rancher Desktop built `ai-gateway-gateway:gaps-dd59d54`; Helm revision
415 completed successfully, and pod `ai-gateway-gateway-d85f85c86-7h2hd`
became Ready with zero restarts. The live endpoint confirmed compatible create
durations, sizes and image URL references, the narrower native xAI size set,
and xAI extension durations.

Source `10ab0de` adds execution-validator-derived native Interactions profiles.
Rancher Desktop built `ai-gateway-gateway:gaps-10ab0de`; Helm revision 416
completed successfully, and pod `ai-gateway-gateway-69c86f89b-v2lw5` became
Ready with zero restarts. The live endpoint confirmed Gemini string/step input,
agent and environment lifecycle controls, streaming/background flags, structured
output, bounded generation controls and all four accepted thinking levels.

Source `94551d0` adds Rerank to durable JSONL batch execution. Rancher Desktop
built `ai-gateway-gateway:gaps-94551d0`; Helm revision 417 completed
successfully, and pod `ai-gateway-gateway-784dcb8fbf-ncckj` became Ready with
zero restarts. The live OpenAPI 0.1.318 contract lists `/v1/rerank` in the
accepted batch endpoints; the regression executes it through durable input and
output files, shared validation, model policy, TPM admission and routing.

Source `2ab8c57` adds Search to durable JSONL batch execution. Rancher Desktop
built `ai-gateway-gateway:gaps-2ab8c57`; Helm revision 418 completed
successfully, and pod `ai-gateway-gateway-6df7cfd5cf-zw2lj` became Ready with
zero restarts. The live OpenAPI 0.1.319 contract lists `/v1/search` in the
accepted batch endpoints; the regression covers multi-query execution through
shared validation, policy-visible messages, TPM admission and search-unit usage.

Source `59bc7b1` adds Image Generation to durable JSONL batch execution. Rancher
Desktop built `ai-gateway-gateway:gaps-59bc7b1`; Helm revision 419 completed
successfully, and pod `ai-gateway-gateway-6bb85597f4-8r67p` became Ready with
zero restarts. The live OpenAPI 0.1.320 contract lists
`/v1/images/generations` in the accepted batch endpoints; the regression covers
shared validation, model policy, prompt/output reserve and usage-bearing output.

Source `a814059` adds bounded Image Generation SSE streaming with validated
partial/completed ordering, pre-first-event retry and fallback, and exact final
usage settlement. Rancher Desktop built `ai-gateway-gateway:gaps-a814059`; Helm
revision 420 completed successfully, and pod
`ai-gateway-gateway-745c954bdf-v9rkp` became Ready with zero restarts. The live
OpenAPI 0.1.321 contract exposes `stream`, `partial_images` and both event
schemas; live validation rejected `partial_images` without `stream=true` before
provider execution.

Source `e6ebb6b` adds bounded Image Edit SSE streaming through the existing
multipart validation and content-policy lifecycle. Rancher Desktop built
`ai-gateway-gateway:gaps-e6ebb6b`; Helm revision 421 completed successfully,
and pod `ai-gateway-gateway-7f9d8bdc5c-8tm5n` became Ready with zero restarts.
The live OpenAPI 0.1.322 contract exposes Image Edit streaming controls and both
event schemas; live multipart validation rejected `partial_images` without
`stream=true` before authentication or provider execution.

Source `5f583f6` adds bounded Audio Transcription SSE streaming with strict
delta/segment/done lifecycle validation, pre-first-event fallback and exact
terminal usage settlement. Rancher Desktop built
`ai-gateway-gateway:gaps-5f583f6`; Helm revision 423 completed successfully,
and pod `ai-gateway-gateway-85d76b6c66-wf5jj` became Ready with zero restarts.
The live OpenAPI 0.1.323 contract exposes the transcription stream flag and all
three event schemas; live multipart validation rejected streaming Audio
Translation with `invalid_request` before authentication or provider execution.

Source `6795aff` adds bounded Text-to-Speech SSE streaming with strict base64
audio delta validation, terminal exact usage, pre-first-event fallback and
post-response settlement. Rancher Desktop built
`ai-gateway-gateway:gaps-6795aff`; Helm revision 424 completed successfully,
and pod `ai-gateway-gateway-646df8f87-g5vx6` became Ready with zero restarts.
The live OpenAPI 0.1.324 contract exposes both speech event schemas and explicit
per-adapter `sse_supported`; live requests accepted `stream_format=sse` into the
authentication lifecycle and rejected an unknown stream format with
`invalid_request` before authentication or provider execution.

Source `6fe2c2f` adds Text-to-Speech to durable JSONL batch execution. The
regressions cover shared request validation, policy snapshot model authorization,
speech TPM reservation, exact character attribution, independent execution IDs and
base64 audio output with content type and usage. SSE requests are rejected before
provider execution. Batch creation now reserves enough output capacity for a bounded
result line per item, while an oversized provider result becomes a terminal
`batch_result_too_large` error instead of leaving the batch stuck in finalization.

Rancher Desktop built `ai-gateway-gateway:gaps-6fe2c2f` with image ID
`sha256:7862e79770800fd6f2b0fe85875a1e4621e01cfd6742990c84d461e59699d014`.
Helm revision 425 completed successfully, and pod
`ai-gateway-gateway-6b57c49967-xcwg9` became Ready with zero restarts. The live
OpenAPI 0.1.325 contract lists `/v1/audio/speech` in the accepted batch
endpoints. An unauthenticated request using that endpoint reached authentication
with 401, while an unknown batch endpoint was rejected by request validation
with 400.

Source `3c8d423` adds Response Compact to durable JSONL batch execution. The
regression runs a compact request through shared strict validation, model policy,
TPM admission, independent execution identity, provider retry and post-response
billing, and verifies usage-bearing compact output. Empty input and unknown
request fields fail before a job is queued.

Rancher Desktop built `ai-gateway-gateway:gaps-3c8d423` with image ID
`sha256:870163902961668bdda449abce2a2563e22cf39e571fa049c64e160d7ff95b95`.
Helm revision 426 completed successfully, and pod
`ai-gateway-gateway-868db9ff96-mjs5j` became Ready with zero restarts. The live
OpenAPI 0.1.326 contract lists `/v1/responses/compact` in the accepted batch
endpoints. An unauthenticated request using that endpoint reached authentication
with 401, while a misspelled endpoint failed request validation with 400.

Source `d567cba` adds OCR to durable JSONL batch execution. Owner-scoped file
references are resolved and signature-validated before queueing, and the resolved
inline document is processed by the usual OCR content-policy and provider lifecycle.
The regressions cover cross-owner denial, page reservation, TPM admission,
independent execution identity and usage-bearing output.

Rancher Desktop built `ai-gateway-gateway:gaps-d567cba` with image ID
`sha256:5091fe684e51d6da065a01a786ac13178c0cdcd2be348ab18285c40be4f9dca6`.
Helm revision 427 completed successfully, and pod
`ai-gateway-gateway-55555ccbf9-kzm9v` became Ready with zero restarts. The live
OpenAPI 0.1.327 contract lists `/v1/ocr` in the accepted batch endpoints. An
unauthenticated request using that endpoint reached authentication with 401,
while an unknown OCR batch endpoint failed request validation with 400.

The Audio Translation streaming item was removed from the gap list after
contract revalidation. The public create operation documents synchronous
multipart output in `json`, `text`, `srt`, `verbose_json` or `vtt` form and does
not define a streaming request or event lifecycle. The existing regression
therefore retains the fail-closed rejection of `stream=true` before provider
execution. The Image Generation row was also corrected after confirming that
Image Edit streaming had already shipped and was covered by its own regressions.

Native GenerateContent code execution is isolated behind the
`gemini_code_execution` deployment capability and the `code_execution` tool ACL.
Requests bypass exact and semantic response caches, execution history contributes
to TPM admission, and provider-generated code/result parts are bounded to 128 parts
and 1 MiB before they can be returned or reused. JSON and SSE regressions cover
native transport, history preservation, routing, authorization and final usage
settlement.

Rancher Desktop built `ai-gateway-gateway:gaps-2589b6e` with image ID
`sha256:6cef767a8311ef09c5164896cbd56a725fbdafd9125de29a3749be15c6c4dfb0`.
Helm revision 428 completed successfully, and pod
`ai-gateway-gateway-945b85d48-qd9vw` became Ready with zero restarts. The live
OpenAPI contract reported version 0.1.328, and an unauthenticated native
GenerateContent request containing `codeExecution` reached authentication and
returned the native 401 error envelope.

Native GenerateContent inline audio now accepts the signature-verifiable WAV,
MP3/MPEG, AIFF, AAC, OGG/Opus, FLAC, M4A and WebM containers. Every format passes
the same decoded-size limit, container signature check, AV projection, TPM reserve,
`audio_input` routing requirement and response-cache exclusion. Regression tests
cover decoding into the shared attachment model and the native provider mapping.

Rancher Desktop built `ai-gateway-gateway:gaps-83266a0` with image ID
`sha256:548dbde89eaeb98ba09a16e82a0b5dc7dcaaa85e4c60638a540d744c0e7916e6`.
Helm revision 429 completed successfully, and pod
`ai-gateway-gateway-5bbb7db899-jg2qk` became Ready with zero restarts. The pod's
live endpoint served OpenAPI 0.1.329, and an unauthenticated GenerateContent
request containing signature-valid AAC reached authentication and returned 401.

Native Gemini transcription accepts `mode=VERBATIM|SMART` through the bounded
multipart request model. The selected mode contributes to TPM admission, is sent
in `audioTranscriptionConfig`, and is exposed only in the supporting adapter's
capability profile. Translation and all other adapters reject the field before
network execution. Regressions cover validation, multipart decoding, native
mapping, parameter isolation and capability discovery.

Rancher Desktop built `ai-gateway-gateway:gaps-cc285ee` with image ID
`sha256:d6262b8a8b80b8fe42d5df05c3040ed93cb986b8e599ec92f55c8dd4b2e38959`.
Helm revision 430 completed successfully, and pod
`ai-gateway-gateway-f9b84445b-8t7dq` became Ready with zero restarts. The live
endpoint served OpenAPI 0.1.330 with both transcription modes. A signature-valid
WAV multipart request using `mode=SMART` reached authentication and returned 401.

The shared audio multipart decoder and native Gemini transcription/translation
adapter now accept signature-verifiable AIFF, AAC, Opus and M4A in addition to
the previously supported containers. Generic Ogg and MP4 detector results are
matched to their explicit file extensions only after container signature
validation. Native aliases are normalized to documented MIME types before the
provider call. Raw formats that require sample metadata remain rejected.

Rancher Desktop built `ai-gateway-gateway:gaps-2d6f670` with image ID
`sha256:18ff761547dbd5eef7cd7789475b285014e4aa03eff47921329a4385a9c9558b`.
Helm revision 431 completed successfully, and pod
`ai-gateway-gateway-79d9f68ff4-rc9rq` became Ready with zero restarts. The live
endpoint served OpenAPI 0.1.331 with the expanded format list. A signature-valid
AAC multipart request reached authentication and returned 401.

Native Gemini transcription now maps word timestamps and speaker diarization
to the public bounded words and segments structures. Requested annotations are
mandatory in the provider response; offsets, word ranges and speaker labels are
validated before billing settlement. SMART mode and custom vocabulary conflicts
fail before network execution, and capability discovery now reports timestamps.

Rancher Desktop built `ai-gateway-gateway:gaps-f8c62a8` with image ID
`sha256:d057557751a000dcd54020cb253855918e4cdf1f3c2e59199bda03a90dfdf968`.
Helm revision 432 completed successfully, and pod
`ai-gateway-gateway-59ddf4b965-xzhl6` became Ready with zero restarts. Its live
endpoint served OpenAPI 0.1.332.

Source `3bfad0f` adds Audio Transcription and Audio Translation to durable JSONL
batch execution. Inline base64 audio remains signature-validated and bounded by
the 4 MiB line limit; streaming is rejected before queueing. Each item retains an
independent execution ID, applies TPM admission and returns exact token or duration
usage through the normal provider settlement lifecycle.

Rancher Desktop built `ai-gateway-gateway:gaps-3bfad0f` with image ID
`sha256:130d962774bf5a58a6a74182e405b351ffb0c48f8dfe4274b93c23f9dc5662af`.
Helm revision 434 completed successfully, and pod
`ai-gateway-gateway-865f6d4dd5-5fwfw` became Ready with zero restarts. Its live
endpoint served OpenAPI 0.1.334 with both audio batch endpoints. An unauthenticated
request using `/v1/audio/transcriptions` reached authentication with 401, while an
unknown audio batch endpoint was rejected by request validation with 400.

Source `4c8938c` adds Image Edit and Image Variation to durable JSONL batch
execution. Structured inline images pass the shared signature and size checks,
provider content policy, TPM admission and exact token settlement. Streaming edit
requests are rejected before queueing, and the 4 MiB line limit bounds each item.

Rancher Desktop built `ai-gateway-gateway:gaps-4c8938c` with image ID
`sha256:4102049c961b8e6a377b133654f95f6308a0b58609d577419e57825fcb6fe0d4`.
Helm revision 435 completed successfully, and pod
`ai-gateway-gateway-776b6f897-ndnrx` became Ready with zero restarts. Its live
endpoint served OpenAPI 0.1.335 with both image batch endpoints, and an
unauthenticated `/v1/images/edits` batch request reached authentication with 401.

Source `b0969ce` adds native Messages requests to durable JSONL batch execution.
Each item is converted through the same strict request path as `/v1/messages`,
including tool schemas in TPM admission, then returns a validated native Messages
response after provider billing settlement. Streaming requests are rejected before
queueing.

Rancher Desktop built `ai-gateway-gateway:gaps-b0969ce` with image ID
`sha256:ec04f59d1ddec711673c9933dafcb819b13dc95b0d73ab08c9cafd0d310c1fc6`.
Helm revision 436 completed successfully, and pod
`ai-gateway-gateway-85cfbd6db7-lskq9` became Ready with zero restarts. Its live
endpoint served OpenAPI 0.1.336 with `/v1/messages` in the batch endpoint enum,
and an unauthenticated request using it reached authentication with 401.

Source `c54324c` adds native skill execution to non-streaming Messages and
durable Messages batches. Custom references are resolved by the authenticated
credential and user, pin execution to their creation deployment, and fail closed
for foreign ownership or mixed deployments. Skill and code-execution identifiers
pass tool ACLs, the container reference contributes to TPM reserve, and exact
provider code-execution counts settle as billing tool requests. Stateful execution
bypasses exact and semantic caches.

Rancher Desktop built `ai-gateway-gateway:gaps-c54324c` with image ID
`sha256:2abe2e186fba26c4e83ad301eb2b2b4b4489b214c422e6cd12d49548511f91f1`.
Helm revision 437 completed successfully, and pod
`ai-gateway-gateway-5498c4b48c-v6hjz` became Ready with zero restarts. Its live
endpoint served OpenAPI 0.1.337. A structurally valid native skill request through
the local ingress reached authentication and returned 401.

Source `aaaa437` adds durable native skill container reuse. Provider-issued
container IDs are stored with credential-and-user ownership, deployment affinity
and provider-reported expiry. Unknown, foreign and expired continuations fail
before provider execution; successful continuations remain on the original
deployment. Stateful skill requests bypass response caches, and expired bindings
are removed in bounded batches during writes. Regression coverage includes wire
forwarding, owner isolation, invalid provider responses, collision handling,
expiry and cleanup on a real PostgreSQL database.

Rancher Desktop built `ai-gateway-gateway:gaps-aaaa437` with image ID
`sha256:c1760c5b44cfc61f386c1b20e5f547dff8116e591bfbe8c04d25413605e7d23f`.
PostgreSQL Helm revision 30 applied migration 033 and exposed the new execution
binding table. Gateway Helm revision 438 completed successfully, and pod
`ai-gateway-gateway-59cfc97cbf-dhltt` became Ready with zero restarts. Its live
endpoint served OpenAPI 0.1.338. A structurally valid container continuation
request through the local ingress reached authentication and returned 401.

Source `5b57df0` extends native Messages token counting with skill execution
context. The endpoint validates and authorizes skill references and managed code
execution, resolves custom skill and reusable container ownership, pins the native
counter to the recorded deployment, and sends the complete container and tool
context upstream without opening a generation billing lifecycle. Gateway and
provider wire regressions cover the effective request, ACLs, deployment binding
and native capability header.

Rancher Desktop built `ai-gateway-gateway:gaps-5b57df0` with image ID
`sha256:cb39069dc50a3f749b681b4ebe0a0d9818d3cd145cdb13d4b0e1d7d27451b0fe`.
Gateway Helm revision 439 completed successfully, and pod
`ai-gateway-gateway-56f97d58d6-w8xmh` became Ready with zero restarts. Its live
endpoint served OpenAPI 0.1.339. A structurally valid skill-aware token-count
request through the local ingress reached authentication and returned 401.

Source `e372710` adds native regex and BM25 tool search to Messages. Deferred
function schemas require the native server tool, explicit deployment capability
and tool authorization. Native continuation blocks remain bounded and contribute
to TPM reserve, while exact and semantic caches are disabled. Provider responses
cannot introduce an unrequested tool-search execution. Regression coverage checks
request validation, ACLs, capability routing, provider wire conversion, response
validation, cache exclusion and ordered continuation round trips.

Rancher Desktop built `ai-gateway-gateway:gaps-e372710` with image ID
`sha256:b5171dcef2aa050ee76174b57d18e70ca5fd0f52584f7de1f1cced270714628b`.
Gateway Helm revision 440 completed successfully, and pod
`ai-gateway-gateway-84cd8886db-99jn4` became Ready with zero restarts. Its live
endpoint served OpenAPI 0.1.340 with both tool-search variants and the
`tool_search` capability. A structurally valid tool-search request through the
local ingress reached authentication and returned 401.

Source `c9d9dee` adds the current dynamic-filtering web-search and web-fetch
variants to Messages. Version-gated caller, domain, cache-bypass and response
inclusion controls are validated before provider execution. Native flat search
location fields are forwarded correctly while the former nested shape remains
accepted for compatibility. Web search, web fetch and code execution definitions
now contribute to TPM and budget reserve. Full test, race, vet and build checks
completed successfully.

Rancher Desktop built `ai-gateway-gateway:gaps-c9d9dee` with image ID
`sha256:8910b8af6074176f525616d053782dd2f6bcdd148fefe5a1455bf69b5efb13e5`.
Gateway Helm revision 441 completed successfully, and pod
`ai-gateway-gateway-5b549c96c4-59k4r` became Ready with zero restarts. Its live
endpoint served OpenAPI 0.1.341 with all supported web-tool versions. A valid
20260318 web-search request using flat location fields reached authentication and
returned 401.

Source `b4968ba` preserves the requested native code-execution version through
Messages generation and token counting. The 20250825, 20260120 and 20260521
variants share the existing tool ACL, capability routing, cache exclusion,
bounded result validation and exact provider-reported tool usage settlement.
Full test, race, vet and build checks completed successfully.

Rancher Desktop built `ai-gateway-gateway:gaps-b4968ba` with image ID
`sha256:dff41c877752054848aff8a0eb70743075ff299a5d5fc11f5f1836d718e689d0`.
Gateway Helm revision 442 completed successfully, and pod
`ai-gateway-gateway-5877d7f96f-p59jb` became Ready with zero restarts. Its live
endpoint served OpenAPI 0.1.342 with all three code-execution versions. A valid
20260521 request reached authentication and returned 401.

Source `527f76c` adds provider-defined memory, bash and versioned text-editor
client tools to Messages generation, native token counting and durable batches.
Each fixed tool name is authorized independently, deployment routing requires
the corresponding capability, native definitions contribute to TPM reserve, and
exact and semantic response caches are disabled. The caller remains responsible
for executing the returned operation. Full Go test, race, vet and build checks
and all 166 UI tests completed successfully.

Rancher Desktop built `ai-gateway-gateway:gaps-527f76c` with image ID
`sha256:e714edb05df6641ef58d4167231b86907df68fc0946a5de3447548af6c846b0d`.
Gateway Helm revision 443 completed successfully, and pod
`ai-gateway-gateway-59bc8886c9-j5ws8` became Ready with zero restarts. Its live
endpoint served OpenAPI 0.1.343 with memory, bash and both text-editor contracts.
A valid request containing all three current client-tool families reached
authentication and returned 401.

Source `89c92e3` adds the current computer client toolset with strict validation
of all 17 fixed members, namespaced per-member authorization, capability-based
routing, conservative native token reserve, cache exclusion and native
continuation through JSON and SSE. Mixed server-tool and computer-tool turns,
image and error results, upstream streaming, token counting and durable batch
admission use the same bounded representation. Full Go test, race, vet and build
checks and all 166 UI tests completed successfully.

Rancher Desktop built `ai-gateway-gateway:gaps-89c92e3` with image ID
`sha256:aa28aba0e947bf16c2fca4a7dd6bfa34b38f7ee19ebc43fa7627db15fe5f74c1`.
Gateway Helm revision 444 completed successfully, and pod
`ai-gateway-gateway-cd6b96986-jxmz8` became Ready with zero restarts and the
same image digest. Its live endpoint served OpenAPI 0.1.344 with the current
computer toolset and all 17 fixed member configs. A structurally valid request
with a disabled `zoom` member reached authentication and returned 401.

Source `261b4d3` adds the current browser client toolset with all 31 fixed
members, provider-specific capability routing, namespaced action authorization,
conservative context reserve, native token counting and response-cache
exclusion. The four high-risk or optional executor actions remain disabled by
default. Browser continuation validates bounded tab inventories, active-tab
consistency, tab-open and download events, state-only tab-management results,
capture images and error isolation. Browser and computer members with the same
name remain distinct through request history and JSON/SSE output. Full Go test,
race, vet and build checks and all 166 UI tests completed successfully.

Rancher Desktop built `ai-gateway-gateway:gaps-261b4d3` with image ID
`sha256:5b31152bec2a109a7c1bb374de2b8699d47d833830096a8d33b735d750539929`.
Gateway Helm revision 445 completed successfully, and pod
`ai-gateway-gateway-86969fcbdc-xrfgw` became Ready with zero restarts and the
same image digest. Its live endpoint served OpenAPI 0.1.345 with the browser
toolset and representative default and opt-in member configs. A structurally
valid request enabling `read_console` reached authentication and returned 401.

Source `2697f59` accepts `stream=true` for owner-checked native skill execution.
The gateway buffers the provider result, validates and durably stores the
returned container owner, expiry and deployment binding, and only then emits the
Messages SSE sequence with the container descriptor in `message_start`. Invalid
or unpersistable containers fail before SSE begins. JSON execution, reusable
container affinity, tool ACL, TPM reserve and exact settlement remain unchanged.
Full Go test, race, vet and build checks completed successfully.

Rancher Desktop built `ai-gateway-gateway:gaps-2697f59` with image ID
`sha256:156109508a5d95f44d641b2006d02d7cc4c85452a37445e0dc0ff94e65196dba`.
Gateway Helm revision 446 completed successfully, and pod
`ai-gateway-gateway-8fbd75755-9dkcg` became Ready with zero restarts and the same
image digest. Its live endpoint served OpenAPI 0.1.346 with the buffered skill
SSE contract. A structurally valid `stream=true` built-in skill request reached
authentication and returned 401.

Source `fd09e6d` adds native Messages thinking configuration in adaptive,
disabled and legacy budgeted modes. Validation enforces mode-specific fields,
manual budget and temperature constraints. Routing requires an explicit native
thinking capability, response caches are bypassed, and the existing max_tokens
reserve remains the total output bound. Follow-up source `5e132c4` preserves the
same policy through the public native token-count endpoint. Generation, token
counting and durable batches therefore use the same validated provider context.
Full Go test, race, vet and build checks and all 166 UI tests completed
successfully.

Rancher Desktop built `ai-gateway-gateway:gaps-5e132c4` with image ID
`sha256:c3c46a713cacb8e0ba790eedc354d66e20c750d61c0fa12f8dcfa7c8b00964ab`.
Gateway Helm revision 447 completed successfully, and pod
`ai-gateway-gateway-758664b478-mwslm` became Ready with zero restarts and the
same image digest. Its live endpoint served OpenAPI 0.1.347 with all three
thinking modes and their constraints. A valid adaptive generation request and
a manual-budget token-count request passed structural validation and reached
authentication. An enabled budget equal to `max_tokens` failed before
authentication with the documented 400 response.

Source `784c591` forwards the bounded native `top_k` sampling control through
Messages and Chat generation on the Anthropic adapter. The adapter parameter
profile now advertises the accepted field, while upstream model-specific
rejection remains explicit and terminal. Existing cache identity, quota reserve,
retry and billing paths already include the shared generation options. Full Go
test, race, vet and build checks completed successfully.

Source `07688c9` adds an OpenAPI regression that keeps `top_k` scoped to
Messages and excludes it from the unrelated assistant-run request schema.
Rancher Desktop built `ai-gateway-gateway:gaps-07688c9` with image ID
`sha256:21398d4d919fa0d5f729fa33c9f7912ce3badaf7abfcefae343af2fdcaa7c6bc`.
Gateway Helm revision 449 completed successfully, and pod
`ai-gateway-gateway-78f6667f4d-g49mm` became Ready with zero restarts and the
same digest. Live OpenAPI 0.1.348 exposes `top_k` under Messages only. A valid
request reached authentication, while a negative value failed validation with
the documented 400 response.

Source `f522aa0` accepts explicit `max_tokens: 0` only for native Messages and
preserves that value on the Anthropic wire. TPM and billing admission reserve
the complete input context with an exact zero output allowance; ordinary Chat
requests retain their positive-limit validation and default reserve semantics.
Source `9632c14` requires the explicit `zero_output` deployment capability and
advertises it only for the native adapter that preserves this contract. Tests
cover request validation, provider serialization, TPM and billing reserve,
capability validation, managed profiles and fallback isolation. Full Go test,
race, vet and build checks and all 166 UI tests completed successfully.

Rancher Desktop built `ai-gateway-gateway:gaps-61a0475` with image ID
`sha256:fe8845d80d37dcbb22b063578702d8c94136dafe5dbb77e4e004128af53ae9ce`.
Gateway Helm revision 450 completed successfully, and pod
`ai-gateway-gateway-6b9bd5955f-jcv6d` became Ready with zero restarts and the
same digest. Live OpenAPI 0.1.349 documents the Messages zero-output contract.
A versioned `max_tokens: 0` Messages request passed structural validation and
reached authentication, while a negative Messages value and an explicit zero
Chat limit failed validation with 400 responses.

OpenAPI 0.1.350 removes `background` and `stream_options` from Messages because
the strict request decoder does not implement either field. A schema regression
prevents these unsupported lifecycle controls from being advertised again;
background Messages and client-controlled stream options remain explicit API
coverage gaps.

Rancher Desktop built `ai-gateway-gateway:gaps-c179c4a` with image ID
`sha256:3000ee3fe21294a84e3f1ce12c24e38bc3cd28bdadd7035d43304d5699796456`.
Gateway Helm revision 451 completed successfully, and pod
`ai-gateway-gateway-694449765c-vbt54` became Ready with zero restarts and the
same digest. Live OpenAPI 0.1.350 omits both unsupported Messages fields while
retaining the tested zero-output contract.

Source `6e44c0b` adds native Messages top-level ephemeral prompt-cache control.
Validation combines it with block and tool markers under the existing limit,
routing requires the explicit `prompt_cache` capability, and native generation,
token counting and durable batches preserve the TTL. Exact response-cache keys
include the control and semantic caching is disabled. Full Go test, race, vet
and build checks completed successfully.

Rancher Desktop built `ai-gateway-gateway:gaps-6835f2c` with image ID
`sha256:4e95108197620440cb861ad21fad225ed49b3e216ef9f73e6f5227f2ed92506e`.
Gateway Helm revision 452 completed successfully, and pod
`ai-gateway-gateway-559945f6f8-cc46b` became Ready with zero restarts and the
same digest. Live OpenAPI 0.1.351 exposes the bounded top-level cache control.
A valid one-hour control reached authentication; an unsupported 30-minute TTL
failed request validation with a 400 response.

Source `99f8e7c` adds native Messages inference geography with explicit
`global`/`us` validation and capability-isolated routing. Native generation,
token counting and durable batches preserve the request. JSON and SSE responses
must report the requested value without changing it. Both response caches are
bypassed, and US-only inference applies the provider's 1.1 input/output token
price multiplier before reserve so settlement reuses the same snapshot. The
token-count contract now also preserves the previously documented top-level
prompt-cache control. Full Go tests, race tests, vet and build passed; UI tests,
type checking and the production bundle build passed.

Rancher Desktop built `ai-gateway-gateway:gaps-f191d4b` with image ID
`sha256:5982aff6450e12158e8c25d0765f9c0fb2eb3b4f2df8947aa85ce68de6acb830`.
Gateway Helm revision 453 completed successfully, and pod
`ai-gateway-gateway-67cf68587d-jk2q6` became Ready with zero restarts and the
same digest. Live OpenAPI 0.1.352 exposes inference geography on Messages,
token counting and response usage. Valid `us` generation and `global`
token-count requests passed structural validation and reached authentication;
an unsupported geography returned 400 on both endpoints. These smoke checks
used no credential and performed no external inference.

Source `0f00b98` adds bounded native Messages context editing for tool-result and
thinking-block clearing. Strict request validation bounds thresholds and tool
lists, enforces strategy order and rejects unknown fields. Capability routing
isolates the native adapter, both response caches are bypassed, generation and
durable batches preserve the policy, and token counting returns provider-reported
post-edit and original context sizes. JSON and SSE responses accept only
nonnegative applied-edit counters for strategies requested by the client. TPM
admission remains conservative over the submitted context, while ordinary
provider usage drives settlement. Full Go tests, race tests, vet and build
passed; all 166 UI tests, type checking and production bundle build passed.

Rancher Desktop built `ai-gateway-gateway:gaps-8b15bb3` with image ID
`sha256:078213cfce3c66d5dfe174c9709d2550043662f4b9670c1d218a60cea0e0fad2`.
Gateway Helm revision 454 completed successfully, and pod
`ai-gateway-gateway-5bcfb99c44-8l2fq` became Ready with zero restarts and the
same digest. Live OpenAPI 0.1.353 exposes the bounded context-management
request, response and token-count contracts. Valid generation and token-count
requests passed structural validation and reached authentication. An empty edit
list and tool-result clearing placed before thinking clearing returned 400 with
the documented validation errors. These smoke checks used no credential and
performed no external inference.

Source `1b24b3a` preserves failed ordinary client tool results through the native
Messages generation, token-count and durable-batch paths. Routing requires an
explicit adapter capability, exact cache identity includes the failure flag and
semantic response caching is disabled. Existing successful tool results keep
their previous wire shape and exact-cache identity. Full Go tests, race tests,
vet and build passed; all 166 UI tests, type checking and the production bundle
build passed.

Rancher Desktop built `ai-gateway-gateway:gaps-82aa5a2` with image ID
`sha256:19d73dfda853a2ed089dbb626f137fac32fcae3fb7f15d31a6a3fff3740d976d`.
Gateway Helm revision 455 completed successfully, and pod
`ai-gateway-gateway-565d997459-rqc48` became Ready with zero restarts and the
same digest. Live OpenAPI 0.1.354 exposes the failed tool-result schema and
capability. A valid `is_error: true` continuation passed structural validation
and reached authentication; a string-valued flag returned 400. These smoke
checks used no credential and performed no external inference.

Source `decd6a3` adds inline base64 PDF blocks to native Messages generation,
token counting and durable batches through the existing bounded file-input
pipeline. Requests accept at most five documents and 16 MiB of decoded PDF data,
validate the container signature, expose the bytes to attachment policy modules,
reserve conservative input tokens, bypass response caches and require explicit
`file_input` routing. Full Go tests, race tests, vet and build passed.

Rancher Desktop built `ai-gateway-gateway:gaps-492dd59` with image ID
`sha256:fd565317dee040b84ffe3c8fdb28271af910098e98f22fac2719b614c60bc0c0`.
Gateway Helm revision 456 completed successfully, and pod
`ai-gateway-gateway-66446f9568-874jt` became Ready with zero restarts and the
same digest. Live OpenAPI 0.1.355 exposes `MessagesDocumentBlock`. A valid
base64 PDF request passed structural validation and reached authentication;
bytes without a PDF signature returned 400 before authentication. These smoke
checks used no credential and performed no external inference.

Source `e737188` adds per-document native citation control to bounded Messages
PDF input. Generation, token counting and durable batches preserve the setting;
routing requires `document_citations`, exact-cache keys isolate it, and semantic
cache reuse is disabled. Requests with `enabled: false` fail validation instead
of being silently reinterpreted. Full Go tests, race tests, vet and build passed,
along with all 166 UI tests, UI type checking and the production UI build.

Rancher Desktop built `ai-gateway-gateway:gaps-eab031b` with image ID
`sha256:07aa3e219c1cf166786408e020541ad8bc3c4634065d866023901d0964ec0aa4`.
Gateway Helm revision 457 completed successfully, and pod
`ai-gateway-gateway-5bf6bf6b5c-j7kv4` became Ready with zero restarts and the
same digest. Live OpenAPI 0.1.356 exposes the citation request contract and
capability. With the required protocol-version header, a PDF request using
`citations.enabled=true` passed structural validation and reached
authentication; `enabled=false` returned 400 before authentication. These
smoke checks used no credential and performed no external inference.

Source `c3ffb67` enforces the provider contract that document citations apply to
all documents in a request or none. Mixed cited and uncited PDF inputs now fail
before provider execution. The focused regression test and full Go tests, race
tests, vet and build passed.

Rancher Desktop built `ai-gateway-gateway:gaps-cf64d6e` with image ID
`sha256:b797a371b9fa54e1a228381e72a0919badf8506fa85ba27d4c6fee65d9751927`.
Gateway Helm revision 458 completed successfully, and pod
`ai-gateway-gateway-78c5f44bdc-t5wb5` became Ready with zero restarts and the
same digest. Live OpenAPI 0.1.357 documents the all-or-none rule. A request with
two cited PDFs reached authentication, while a mixed cited and uncited request
returned 400 before authentication. These smoke checks used no credential and
performed no external inference.

Source `93cdfc8` makes native Messages capability discovery cumulative across
the complete message list. A failed tool result can no longer stop discovery
before a later cited document, so routing requires both `tool_result_error` and
`document_citations` when both semantics are present. The focused regression
test and full Go tests, race tests, vet and build passed.

Source `48ad345` adds bounded PDF title and context metadata to native Messages.
The fields are projected to DLP, transformed by anonymization, counted during
admission, preserved by native generation, token counting and durable batches,
and isolated by an explicit `document_metadata` capability. Exact cache keys
include the metadata and semantic cache reuse is disabled. Full Go tests, race
tests, vet and build passed, along with all 166 UI tests, UI type checking and
the production UI build.

Rancher Desktop built `ai-gateway-gateway:gaps-35bfb54` with image ID
`sha256:255dca8b439d0c6c11118fcffa51bada9af65fa7ffc4e15d6cd0092e37170573`.
Gateway Helm revision 459 completed successfully, and pod
`ai-gateway-gateway-79d7cdf5db-d2tw7` became Ready with zero restarts and the
same digest. Live OpenAPI 0.1.358 exposes bounded document title/context and the
`document_metadata` capability. A valid metadata request passed structural
validation and reached authentication; a whitespace-only title returned 400
before authentication. The image also contains cumulative native capability
discovery from source `93cdfc8`. These smoke checks used no credential and
performed no external inference.

Source `175793a` adds bounded inline plain-text documents to native Messages.
Each document is limited to 262,144 Unicode characters and the request aggregate
to 1,048,576 characters, with the existing five-document limit shared across
PDF and text input. Text enters DLP and anonymization, TPM and budget reserve,
native generation and token counting, durable batches and explicit
`document_text` capability routing. Exact and semantic response caches are
bypassed. Full Go tests, race tests, vet and build passed, along with all 166 UI
tests, UI type checking and the production UI build.

Rancher Desktop built `ai-gateway-gateway:gaps-c0ab758` with image ID
`sha256:450e29b1fdfac2cce30efd3b4bb9b952b0d8f9c3a3779a18a4ad7dd1feb6f1fd`.
Gateway Helm revision 460 completed successfully, and pod
`ai-gateway-gateway-5f65684d75-9vh6z` became Ready with zero restarts and the
same digest. Live OpenAPI 0.1.359 exposes the PDF/plain-text source variants and
the `document_text` capability. A valid text document with title, context and
citations passed structural validation and reached authentication; a
whitespace-only document returned 400 before authentication. These smoke checks
used no credential and performed no external inference.

Source `d31886a` resolves owner-scoped `user_data` file references for native
Messages PDF and plain-text documents. Authentication establishes credential and
user ownership before the file is read; resolved content then enters DLP,
anonymization, TPM, billing and capability routing. Token counting runs the same
policy without a billing lifecycle. Durable batch creation stores resolved bytes
within the 4 MiB line bound, so expiry or deletion cannot change queued input.
Foreign, expired, malformed and unsupported files fail closed without revealing
which condition occurred. Full Go tests, race tests, vet and build passed.

Rancher Desktop built `ai-gateway-gateway:gaps-987d3ea` with image ID
`sha256:5b5b3c9ba40f2ddf78f19d6537b70e77d452711d8d67569da4b66d4920eb0bc5`.
Gateway Helm revision 461 completed successfully, and pod
`ai-gateway-gateway-85c95dd9f6-xmqgx` became Ready with zero restarts and the
same digest. Live OpenAPI 0.1.360 exposes the owner-scoped file source. A
well-formed reference passed request validation and reached authentication;
an invalid identifier returned 400 before authentication or storage access.
These smoke checks used no credential and performed no external inference.

Source `1084ddb` adds bounded HTTPS PDF documents to native Messages. URLs are
fetched only after authentication through the public-address HTTP transport,
without client credentials, environment proxies or redirects. The transport
requires TLS 1.2 or newer, checks all resolved addresses, has a 15-second timeout
and reads at most 16 MiB. MIME and PDF signature validation complete before DLP,
TPM, billing or provider execution. Token counting uses the same path without
generation billing, and durable batch creation stores immutable resolved bytes.
Focused regressions and the full Go tests, race tests, vet and build passed.

Rancher Desktop built `ai-gateway-gateway:gaps-349ad01` with image ID
`sha256:def754146d86e745a6f53e7219bd2c7789f039159a4b86b3cc04b01527c715da`.
Gateway Helm revision 462 completed successfully, and pod
`ai-gateway-gateway-6458658895-h9xqm` became Ready with zero restarts and the
same digest. Live OpenAPI 0.1.361 exposes the bounded HTTPS URL source. A valid
URL document passed structural validation and reached authentication; an HTTP
loopback URL returned 400 before authentication or network access. These smoke
checks used no credential and performed no external inference.

Source `bf1a213` adds bounded HTTPS URL images to native Messages generation,
token counting and durable batches. Fetching occurs after authentication through
the public-address transport without forwarding client credentials. JPEG, PNG,
GIF and WebP responses are bounded to 8 MiB each and 16 MiB per request, checked
against their declared MIME signature and converted to inline input before
policy, TPM, cache isolation, routing and billing. Batch creation snapshots the
resolved bytes. Focused regressions and the full Go tests, race tests, vet and
build passed.

Rancher Desktop built `ai-gateway-gateway:gaps-8ec020b` with image ID
`sha256:91b34b1de08d13f3c259f2c614b4ccd27aee1e5ca5802174ce9f27b4bc6721a2`.
Gateway Helm revision 463 completed successfully, and pod
`ai-gateway-gateway-569d897479-52m9s` became Ready with zero restarts and the
same digest. Live OpenAPI 0.1.362 exposes the URL image source and bounds. A
valid URL image passed structural validation and reached authentication; an
HTTP loopback URL returned 400 before authentication or network access. These
smoke checks used no credential and performed no external inference.

Source `eef22c2` adds owner-scoped stored image references to native Messages.
The Files object must have `purpose=user_data`, belong to the authenticated
credential and user, and contain a supported JPEG, PNG, GIF or WebP. Resolution
precedes policy, TPM and billing while preserving the existing signature,
per-image and aggregate byte checks. Native token counting follows the same path
without generation billing. Durable batch creation stores immutable resolved
bytes so expiry or deletion cannot change execution. Foreign and unavailable
files remain indistinguishable. Focused regressions and the full Go tests, race
tests, vet and build passed.

Rancher Desktop built `ai-gateway-gateway:gaps-9e1fc26` with image ID
`sha256:64ecd367d4fb1857af48fc389d444c2f31b5197bf555e52691c3619c3bdb04bf`.
Gateway Helm revision 464 completed successfully, and pod
`ai-gateway-gateway-84cd469fc7-l28ll` became Ready with zero restarts and the
same digest. Live OpenAPI 0.1.363 exposes the owner-scoped image file source. A
well-formed reference reached authentication, while an invalid identifier
returned 400 before authentication or file storage access. These smoke checks
used no credential and performed no external inference.

Source `5fb2560` adds the native Messages Batch lifecycle over the existing
durable batch engine. Creation validates up to 50,000 native request envelopes,
model and tool policy, streaming exclusion, and resolved file or HTTPS input
before atomically storing immutable items and jobs. Each item receives a unique
execution ID and runs through the normal quota, routing and billing lifecycle.
List and retrieve are scoped to the authenticated credential and user; ordered
JSONL results become available only after completion, cancellation, expiry or
failure. Only terminal batches can be deleted, and PostgreSQL removes their
items and remaining jobs in the same transaction. Regression tests cover native
success and cancellation results, owner isolation, pagination, invalid input,
active-delete conflicts, terminal deletion and job cleanup. Focused tests,
OpenAPI validation, full Go tests, full race tests, vet and build passed. The
PostgreSQL batch lifecycle tests also passed against an isolated schema in the
local Rancher Desktop database.

Rancher Desktop built `ai-gateway-gateway:gaps-a71d455` with image ID
`sha256:4781b73e462680e0bd36acfc3a653eb254b48cdd03165e6b9df8b21206840ce4`.
Gateway Helm revision 465 completed successfully, and pod
`ai-gateway-gateway-5944694776-q7vwl` became Ready with zero restarts and the
same digest. Live OpenAPI 0.1.364 exposes all six native Messages Batch
operations. A structurally valid request reached authentication and returned
401 for an invalid smoke key; a request without the required protocol version
returned 400. These smoke checks created no batch and performed no external
inference.

Source `64e4029` adds owner-scoped `fileData` to native GenerateContent JSON,
SSE and countTokens requests. The gateway accepts only its own bounded Files
identifiers and resolves them after authentication under the credential-and-user
owner key. Declared and stored MIME types must match. Images, PDF/plain text,
signature-verifiable audio and MP4/WebM video are converted to the same internal
forms as validated inline input, including aggregate attachment limits. Resolved
content then enters policy scanning, TPM admission, capability routing, cache
isolation and generation billing; token counting runs the same pre-inference path
without opening billing. Regressions cover every supported media family,
counting, authentication order, foreign ownership and MIME mismatch. Focused and
OpenAPI tests, the full Go suite, full race suite, vet and build passed.

Rancher Desktop built `ai-gateway-gateway:gaps-c4cbfb1` with image ID
`sha256:2ee6774cc0b1b4e9b9b5c3daf0b145a3a1a4ba1b7ab8918121415d34ed302fe4`.
Gateway Helm revision 466 completed successfully, and pod
`ai-gateway-gateway-797458c56-qxbg5` became Ready with zero restarts and the
same digest. Live OpenAPI 0.1.365 exposes the bounded owner-scoped `fileData`
contract. A valid reference shape reached authentication and returned 401 for
an invalid smoke key; an external URI returned 400 before authentication or
storage access. These smoke checks performed no external inference.

Source `038e643` adds write-only bearer credentials for registered MCP servers.
Create and update responses expose only `credential_configured`; omission
preserves the stored value, a new value rotates it and an empty value clears it.
Durable admin state seals credentials with a purpose-specific AEAD key and the
server ID as associated data. The runtime clears the client gateway credential
after authentication and supplies only the registered server credential to the
bounded HTTPS client, which applies it to initialization, notification, tool
discovery and tool calls while refusing redirects and non-public addresses.
The UI supports create, rotate, preserve and explicit clear without reading a
stored secret. Focused regressions, all 167 UI tests, UI typecheck/build, the
full Go suite, full race suite, vet and build passed. OpenAPI 0.1.366 documents
the write-only input and read-only configured state.

Rancher Desktop built `ai-gateway-gateway:gaps-1668250` with image ID
`sha256:4e498d332fc806a20ae1df461c87cfc0028472b4407dc11732592dee61bbc316`.
Gateway Helm revision 467 completed successfully, and pod
`ai-gateway-gateway-7b9584b47f-c62q9` became Ready with zero restarts and the
same digest. Live OpenAPI 0.1.366 exposes the write-only bearer input and
read-only configured flag. An MCP discovery request with an invalid smoke key
returned 401 before registry lookup or upstream network access. The smoke check
stored no credential and performed no external MCP call or model inference.

Source `d247692` adds replica-local MCP session reuse through a fixed-capacity
TTL/LRU pool. The pool holds at most 64 clients, refreshes a ten-minute sliding
TTL on use and hashes endpoint plus server credential for its key. Credential
rotation and clearing therefore create a separate session identity without
placing plaintext credentials in map keys. Protocol or transport failures
invalidate the entry before the next request. A mutex makes concurrent cache
misses create one client; the protocol client serializes session operations.
Regressions cover concurrent single creation, capacity eviction, expiry,
credential isolation, successful request reuse and failure recovery. Focused
tests, the full Go suite, full race suite, vet and build passed.

Rancher Desktop built `ai-gateway-gateway:gaps-0f04fcd` with image ID
`sha256:b61efd80f89115b0b289ce5cdad64df4cf2c293d28f211330ca5feb03b9684f3`.
Gateway Helm revision 468 completed successfully, and pod
`ai-gateway-gateway-cbdc6f686-2wdp7` became Ready with zero restarts and the
same digest. Live OpenAPI remained at 0.1.366 because the session pool changes
no public schema. An MCP discovery request with an invalid smoke key returned
401 before registry or upstream access. No credential was stored and no
external MCP call or model inference was performed.

Source `98c1527` adds bounded legacy MCP HTTP+SSE execution for registered
servers. The client requires the first event to declare a same-origin HTTPS
message endpoint, posts JSON-RPC messages with the registered server credential,
requires HTTP 202 acceptance and receives correlated responses on the long-lived
event stream. Per-line and aggregate event limits prevent multiline SSE growth.
The runtime pool key now includes transport; invalidation, TTL expiry and LRU
eviction close long-lived clients outside the cache mutex. Regressions cover
initialize/list/call, credential headers, unsafe endpoint rejection, event
bounds, transport selection and client closure. OpenAPI 0.1.367, focused tests,
the full Go suite, full race suite, vet and build passed.

Rancher Desktop built `ai-gateway-gateway:gaps-c656c56` with image ID
`sha256:8494438873ce584c237fa09913621a72cd01869246c39c87d7eec8cf6dba6a75`.
Gateway Helm revision 469 completed successfully, and pod
`ai-gateway-gateway-585f6c4767-8wc25` became Ready with zero restarts and the
same digest. Live health returned 204 and OpenAPI 0.1.367 exposes executable
Streamable HTTP and legacy HTTP+SSE transports. An MCP discovery request with
an invalid smoke key returned 401 before registry or upstream access. No
credential was stored and no external MCP call or model inference was
performed.

Source `ab3f47e` adds bounded server-request handling to legacy MCP SSE sessions.
The client answers `ping`, returns JSON-RPC method-not-found for capabilities it
did not advertise, consumes notifications without corrupting the next event and
limits pending requests to eight. IDs are restricted to bounded JSON-RPC string
or number values. Response delivery uses the configured credential and a
30-second cancellable deadline; failure enters the existing session invalidation
path. End-to-end and bound regressions, the full Go suite, full race suite, vet
and build passed.

Rancher Desktop built `ai-gateway-gateway:gaps-a94cf8d` with image ID
`sha256:7703735a39c31df419b8d4b40cb97ed1ec4b0ee530725277c2970295ac5b07a1`.
Gateway Helm revision 470 completed successfully, and pod
`ai-gateway-gateway-56db5854db-cfwxk` became Ready with zero restarts and the
same digest. Live health returned 204 and OpenAPI remained at 0.1.367 because
server-request handling changes no public schema. An MCP discovery request with
an invalid smoke key returned 401 before registry or upstream access. No
credential was stored and no external MCP call or model inference was
performed.

Source `6f200c6` adds capability-isolated Realtime audio over the existing
bounded WebSocket transport. Input append/commit/clear supports PCM16 at 24 kHz
and G.711 mu-law/A-law, validates strict base64 with a 15 MiB decoded per-event
limit, tracks at most 1 GiB of buffered bytes without retaining raw audio and
runs configured AV policy before provider delivery. Manual and server-VAD
commits add a duration-derived audio-token estimate to response TPM and budget
reserve; item deletion removes that context. Output audio requires its own
deployment capability and malformed deltas fail closed. Exact terminal usage
preserves cached, text and audio token details. Focused regressions, the full Go
suite, full race suite, vet and build passed. OpenAPI 0.1.368 records these
bounds and capability requirements.

Rancher Desktop built `ai-gateway-gateway:gaps-b1a0a66` with image ID
`sha256:42eecd2c0ca96b35cb46d5ae0539adce39647054b737e3aa594a5a69b69c2b05`.
Gateway Helm revision 471 completed successfully, and pod
`ai-gateway-gateway-5fdcb55b7d-f4wrz` became Ready with zero restarts and the
same image ID. Live health returned 204 and OpenAPI 0.1.368 exposes the Realtime
audio capability, lifecycle and size contract. A Realtime request with an
invalid smoke key returned 401 before deployment selection or upstream access.
The smoke checks performed no external inference and submitted no audio.

Source `a4dcfe0` closes a provider-event cardinality bypass found during the
post-rollout audit: zero-byte `input_audio_buffer.committed` events now consume
the same 1024-item conversation bound as buffered commits. The focused gateway
test, focused race test, full Go suite, vet and build passed. Rancher Desktop
built `ai-gateway-gateway:gaps-a4dcfe0` with image ID
`sha256:ba068744c0335a689051bfad3bdcf5eb91c6acb2aea83afd2a5d45785872483b`.
Gateway Helm revision 472 completed successfully, and pod
`ai-gateway-gateway-97bd8bdcd-977kt` became Ready with zero restarts and the
same image ID. Live health returned 204, OpenAPI remained at 0.1.368, and an
invalid Realtime smoke credential returned 401 before deployment selection.

Source `8f4e9c3` gives Azure Realtime its native WebSocket URL and authentication
contract. GA sessions use `/openai/v1/realtime` with a model deployment query;
versioned preview sessions use `/openai/realtime` with deployment and API-version
queries. The handshake uses the configured `api-key`, an explicit Entra bearer
token or the existing refreshable ambient managed-identity chain. Missing API-key
credentials fail before dial. Protocol regressions cover both URL variants, all
three credential sources and the unchanged generic Realtime handshake. The full
Go suite, full race suite, vet and build passed.

Rancher Desktop built `ai-gateway-gateway:gaps-1c894af` with image ID
`sha256:c1a4c01d1d3567d9cc4555f2ed6f25a0903c804f7c0d56a249176c8df02bccc9`.
Gateway Helm revision 473 completed successfully, and pod
`ai-gateway-gateway-559496bfdf-8h5kc` became Ready with zero restarts and the
same image ID. Live health returned 204 and OpenAPI remained at 0.1.368. An
invalid Realtime smoke credential returned 401 before deployment selection.
No cloud credential or external inference was used by the smoke checks.

Source `ad74244` closes unaccounted Realtime ASR execution. Input transcription
usage belongs to a separate transcription model and can be token- or
duration-priced, while the existing session tracked only Realtime response
usage. Legacy and current transcription configuration now fails before provider
delivery with an explicit `unsupported_feature`; unexpected provider
transcription events also fail closed. Regressions cover all three configuration
forms, provider completion usage and the full WebSocket boundary. The full Go
suite, full race suite, vet and build passed. OpenAPI 0.1.369 documents the
temporary fail-closed contract until independent ASR admission and settlement
are implemented.

Rancher Desktop built `ai-gateway-gateway:gaps-8b59381` with image ID
`sha256:df97c4647706942fe109aefa96d9535ef110a9f61a894574690679971c2143fe`.
Gateway Helm revision 474 completed successfully, and pod
`ai-gateway-gateway-f669f4665-74cgk` became Ready with zero restarts and the
same image ID. Live health returned 204, OpenAPI reported 0.1.369, and an
unauthenticated Realtime smoke request returned 401 before deployment selection.
The smoke checks submitted no audio and performed no external inference.

Source `b1bf823` adds Azure China workload identity selection for official
`.openai.azure.cn` and `.cognitiveservices.azure.cn` provider endpoints.
Federated identity uses `login.chinacloudapi.cn` with the China Cognitive
Services scope, and managed identity requests the matching China resource
audience. A suffix-boundary regression keeps lookalike external domains on the
public-cloud defaults. Focused regressions, the full Go suite, full race suite,
vet and build passed. OpenAPI 0.1.370 publishes the three-cloud identity
contract.

Rancher Desktop built `ai-gateway-gateway:gaps-1cc7ac8` with image ID
`sha256:202bee65638f878d616981c9cc70537ce94c6dd3e8524e98e190961429af8bcb`.
Gateway Helm revision 475 completed successfully, and pod
`ai-gateway-gateway-54bf8df597-llqrr` became Ready with zero restarts and the
same image ID. Live health returned 204, OpenAPI reported 0.1.370, and an
unauthenticated Models request returned 401 before discovery. No Azure
credential or external provider call was used by the smoke checks.

Source `4f46093` adds the managed `cerebras` provider adapter with bearer
authentication, `/v1/models` discovery, Chat Completions and streaming, function
tools, JSON Schema output, explicit parameter validation and exact upstream
usage. Provider-native reasoning output is bounded and normalized to the public
`reasoning_content` field for JSON and SSE. Unsupported fields fail before the
HTTP request. Focused protocol and capability-profile regressions, the full Go
suite, full race suite, vet, build, UI typecheck, UI production build and all 167
UI tests passed. Contract commit `a457530` publishes the provider in OpenAPI
0.1.371 and documents the verified boundary.

Rancher Desktop built `ai-gateway-gateway:gaps-a457530` with image ID
`sha256:2aef0daec413e92b75e224cee13ac1bbfe582e23271d30a07cce517070e47898`.
Gateway Helm revision 476 completed successfully, and pod
`ai-gateway-gateway-6798c5864b-sdhvh` became Ready with zero restarts and the
same image ID. Live health returned 204, OpenAPI reported 0.1.371, the deployed
capability endpoint exposed the exact Chat/stream, tools, structured-output and
parameter policy, and an unauthenticated Models request returned 401. No
provider credential or external inference was used by the smoke checks.

Source `a70579c` adds a bounded `nvidia-nim` adapter for the documented NIM LLM
runtime APIs. It supports Chat Completions, streaming, legacy Completions,
Responses, Embeddings and model discovery, with optional bearer credentials for
hosted endpoints. Function tools, structured output and model-dependent image,
audio and video input remain explicit deployment capabilities. Keeping the
compatible client private prevents unrelated compatible image, audio, video and
resource lifecycle APIs from leaking into the managed profile. Local protocol
regressions cover all five upstream paths, bearer propagation, exact usage,
sorted discovery, the bounded capability profile and preflight parameter
rejection. The full Go suite, full race suite, vet, build, UI typecheck, UI
production build and all 167 UI tests passed. Contract commit `83d1f26`
publishes the provider in OpenAPI 0.1.372.

Rancher Desktop built `ai-gateway-gateway:gaps-83d1f26` with image ID
`sha256:676f4b802a57f5a0b1a36a4e9db8f7f89b5855fa536a4197ff25c5c2b80ddc59`.
Gateway Helm revision 477 completed successfully, and pod
`ai-gateway-gateway-74ddbc59dc-6gn48` became Ready with zero restarts and the
same image ID. In-pod health succeeded, OpenAPI reported 0.1.372, the deployed
capability endpoint exposed only Chat, Completions, Responses, Embeddings,
streaming, tools, structured output and model-dependent multimodal input, and an
unauthenticated Models request returned 401. No provider credential or external
inference was used by the smoke checks.

Source `96de24a` routes incoming Messages requests to the native NVIDIA NIM
Messages endpoint while ordinary Chat Completions remain on their compatible
endpoint. Native Messages streaming and count-tokens reuse the shared policy,
admission, retry, health and accounting lifecycle. Exact and semantic cache
scopes now include the incoming API contract so equivalent Chat and Messages
payloads cannot reuse results across wire protocols. Regressions cover JSON and
SSE conversion, bearer propagation, exact provider token counts, router
selection and cache isolation. The full Go suite, full race suite, vet and build
passed. Contract commit `657cfc1` publishes the expanded adapter in OpenAPI
0.1.373.

Rancher Desktop built `ai-gateway-gateway:native-messages-657cfc1` with image ID
`sha256:f51879fecb23e8a2bd4a6215bb8167a3dadd8866ac284cb2d9df798447eba9cf`.
Gateway Helm revision 478 completed successfully; only the top-level image tag
changed from revision 477. Pod `ai-gateway-gateway-6df4745965-4fc9p` became Ready
with zero restarts and the same image ID. In-pod health succeeded, OpenAPI
reported 0.1.373, the deployed NVIDIA NIM profile included `count_tokens`, and
an unauthenticated Models request returned 401. No provider credential or
external inference was used by the smoke checks.

Source `f1a39a9` exposes NVIDIA NIM Responses retrieve and cancel transports.
The public lifecycle continues to require the durable owner record and the
original deployment identity before any provider call. Protocol regressions
cover methods, paths, bearer propagation, empty cancellation bodies, response
IDs, status and exact usage. The focused protocol and race tests, full Go suite,
full race suite, vet and build passed. Contract commit `716a829` documents the
lifecycle and publishes OpenAPI 0.1.374.

Rancher Desktop built `ai-gateway-gateway:nim-lifecycle-716a829` with image ID
`sha256:9a4e6886dcb4182792a0d473f2fa276faacd1a050035329003f48e8a0905a77e`.
Gateway Helm revision 479 completed successfully; only the top-level image tag
changed from revision 478. Pod `ai-gateway-gateway-d6d7ddd9f-2t7vz` became Ready
with zero restarts and the same image ID. In-pod health succeeded, OpenAPI
reported 0.1.374, and an unauthenticated response retrieval returned 401. No
provider credential or external inference was used by the smoke checks.

Source `a8dbb43` adds the bounded `together` provider adapter for Chat
Completions, streaming, legacy Completions, Embeddings and model discovery with
bearer authentication. Tools, structured output and vision remain explicit
deployment capabilities. Responses and provider fields known to be accepted
without effect are rejected before HTTP. Protocol regressions cover all four
upstream paths, bearer propagation, exact usage, sorted discovery, profile
boundaries and preflight rejection. The full Go suite, full race suite, vet,
build, UI typecheck, UI production build and all 167 UI tests passed. Contract
commit `17ee35e` publishes the provider in OpenAPI 0.1.375.

The first revision smoke exposed that the generic parameter probe still
advertised incompatible logprob and model-dependent logit-bias controls. Source
`582f799` now rejects those controls and publishes empty logprob modes. Focused
protocol and race tests plus the full Go suite, full race suite, vet and build
passed. Rancher Desktop built `ai-gateway-gateway:together-582f799` with image ID
`sha256:363f2e81cbd95f569884e2e85ca5667450f08db3e3a263e5a5a3c153065157a0`.
Gateway Helm revision 481 completed successfully; only the top-level image tag
changed from revision 480. Pod `ai-gateway-gateway-b7b964498-2d4fl` became Ready
with zero restarts and the same image ID. In-pod health succeeded, OpenAPI
reported 0.1.375, and the live capability profile exposed only the bounded
operations and parameter set. No provider credential or external inference was
used by the smoke checks.

Source `029e7ac` adds native Together Rerank through `/v1/rerank`. Text and
object documents, `top_n` and `return_documents` are preserved, while
unsupported controls fail before HTTP. Provider usage is mandatory, non-negative
and internally consistent before exact billing settlement; responses are bounded
to 8 MiB and reject trailing JSON. Protocol regressions cover bearer propagation,
object documents, capability isolation, invalid or missing usage, trailing data
and oversized responses. Focused protocol and race tests, the full Go suite,
full race suite, vet and build passed. Contract commit `ba2c7ba` publishes the
operation and parameter matrix in OpenAPI 0.1.376.

Rancher Desktop built `ai-gateway-gateway:together-rerank-ba2c7ba` with image ID
`sha256:eaa81c76e86704949db6664802ec6b5844c7ec2ff3026244dda86d9d7e6f3106`.
Gateway Helm revision 482 completed successfully; its manifest differs from
revision 481 only by the top-level image tag. Pod
`ai-gateway-gateway-58cb86b88b-z25jl` became Ready with zero restarts and the
same image ID. Live liveness and readiness returned 204, OpenAPI reported
0.1.376, and the Together capability profile exposed Rerank with only `top_n`,
`return_documents`, text documents and object documents. An unauthenticated
Rerank request returned 401 before deployment selection. No provider credential
or external inference was used by the smoke checks.

Source `e0145e5` adds bounded Together Text-to-Speech through native
`/v1/audio/speech`. The adapter preserves model, input, voice and lowercase
language, maps public PCM to upstream raw audio, sends an explicit MP3 default,
and normalizes a validated binary response to the requested media type.
Instructions, speed, unsupported formats and SSE fail before HTTP. The existing
shared lifecycle records exact Unicode input characters for settlement. Protocol
regressions cover MP3 and PCM request mapping, bearer propagation, media-type
normalization and every rejected option. Focused protocol and race tests, the
full Go suite, full race suite, vet and build passed. Contract commit `363cf3e`
publishes the operation and parameter matrix in OpenAPI 0.1.377.

Rancher Desktop built `ai-gateway-gateway:together-speech-363cf3e` with image ID
`sha256:0542807d9f5abe305bb8551c2cf48c85af353610a704f0289612702c284d5155`.
Gateway Helm revision 483 completed successfully; its manifest differs from
revision 482 only by the top-level image tag. Pod
`ai-gateway-gateway-897c4c58b-rj7zh` became Ready with zero restarts and the same
image ID. Live liveness and readiness returned 204, OpenAPI reported 0.1.377,
and the Together capability profile exposed Text-to-Speech with lowercase
language, response format and binary stream-format controls while keeping SSE
disabled. An unauthenticated speech request returned 401 before deployment
selection. No provider credential or external inference was used by the smoke
checks.

Source `51ead49` adds native Together Audio Transcription through multipart
`/v1/audio/transcriptions`. WAV, FLAC, OGG/Opus, MP3, M4A/MP4 and WebM inputs
must expose a locally verifiable positive duration of at most four hours before
upstream execution. The bounded JSON response receives that exact duration and
duration usage before shared settlement. Parameters whose meaning cannot be
preserved, streaming, AAC without a reliable duration parser and diarization
without public speaker IDs fail before HTTP. Protocol regressions cover the
multipart body, bearer propagation, word and segment output, every rejected
option, response bounds, maximum duration, unsupported audio and exact Router
reserve/settlement. Focused protocol and race tests, the full Go suite, full race
suite, vet and build passed. Contract commit `7dd4316` publishes the operation
and parameter matrix in OpenAPI 0.1.378.

Rancher Desktop built `ai-gateway-gateway:together-transcription-7dd4316` with
image ID
`sha256:3801e77354524289b135ac0b93329c1862130cbf9734dbc4a4d42a4af8ca95b7`.
Gateway Helm revision 484 completed successfully; its manifest differs from
revision 483 only by the top-level image tag. Pod
`ai-gateway-gateway-58b9444664-mc79q` became Ready with zero restarts and the
same image ID. Live liveness and readiness returned 204, OpenAPI reported
0.1.378, and the Together capability profile exposed Audio Transcription with
language, response format, temperature and timestamp controls. An
unauthenticated multipart request containing a valid WAV returned 401 before
deployment selection. No provider credential or external inference was used by
the smoke checks.

Source `ddfa2da` adds native Together Audio Translation through multipart
`/v1/audio/translations`. Prompt bias, JSON or verbose JSON response format and
temperature are preserved; language, timestamps, diarization, batch controls
and streaming fail before HTTP. Translation reuses the transcription
container-duration gate, 4-hour limit, bounded response decoder and exact
duration settlement. Protocol regressions cover the multipart body, bearer
propagation, parameter isolation and exact Router reserve/settlement. Focused
protocol and race tests, the full Go suite, full race suite, vet and build
passed. Contract commit `d468681` publishes the operation and parameter matrix
in OpenAPI 0.1.379.

Rancher Desktop built `ai-gateway-gateway:together-translation-d468681` with
image ID
`sha256:1357fae90bd141a9123195d8eadf8415f64500e6ecfa5e8646dd96e3724675a5`.
Gateway Helm revision 485 completed successfully; its manifest differs from
revision 484 only by the top-level image tag. Pod
`ai-gateway-gateway-5b6fc5754f-4lpm4` became Ready with zero restarts and the
same image ID. Live liveness and readiness returned 204, OpenAPI reported
0.1.379, and the Together capability profile exposed Audio Translation with
prompt, response-format and temperature controls. An unauthenticated multipart
request containing a valid WAV returned 401 before deployment selection. No
provider credential or external inference was used by the smoke checks.

Source `2295897` adds native NVIDIA NIM text Rerank through `/v1/ranking`.
Requests carry one query object and 1 to 512 text passage objects, with optional
`NONE` or `END` truncation. `top_n` is applied only after the adapter validates
the complete ordered ranking, so provider work and token usage remain exact.
Object documents and unsupported chunk/token controls fail before HTTP. The
8 MiB response contract rejects trailing JSON, missing, duplicate, out-of-range
or unordered rankings, and missing, zero or inconsistent provider usage. Router
regressions verify document projection and exact usage propagation into the
post-response lifecycle. Every other managed rerank adapter rejects the
native-only truncation field. Vet, build, the full Go suite and full race suite
passed. Contract commit `824fc56` publishes the operation and parameter matrix
in OpenAPI 0.1.380.

Rancher Desktop built `ai-gateway-gateway:nvidia-rerank-824fc56` with image ID
`sha256:f766df9b2095125c051636932f8cf97a85da7c84bef0ca75528d87ecadd3e54e`.
Gateway Helm revision 486 completed successfully; its manifest differs from
revision 485 only by the top-level image tag. Pod
`ai-gateway-gateway-87675b75f-m2csr` became Ready with zero restarts and the
same image ID. Live liveness and readiness returned 204, and OpenAPI reported
0.1.380 with the bounded `truncate` enum and provider-profile option. An
unauthenticated native Rerank-shaped request returned 401 before deployment
selection. No NVIDIA NIM credential or external inference was used by the smoke
checks.

Source `4665503` adds bounded per-output image pricing for Image Generation,
Edit and Variation. Reserve charges the requested count and commit reconciles
the validated response count. PostgreSQL reservation snapshots preserve the
unit price across retries and settlement; ClickHouse persists output count and
unit price for request logs and usage reports. Unit, UI, full race and required
PostgreSQL integration tests passed against three isolated databases. OpenAPI
0.1.381 exposes the catalog and reporting fields.

Rancher Desktop built `ai-gateway-gateway:image-pricing-4665503` with image ID
`sha256:72b4a7fdb2ef51662e210fda5154fb30c52abcfb995a01ddba8eefa4607c685e`
and `ai-gateway-billing:image-pricing-4665503` with image ID
`sha256:d58184ab0adcd9b31de9641eed185e5fc19090c975ffebe7913ee5eb9de34e98`.
PostgreSQL Helm revision 31 and ClickHouse revision 13 applied the new columns;
billing revision 33 and gateway revision 487 became Ready with zero restarts.
Live liveness and readiness returned 204, both schema columns were verified in
each database, and the served OpenAPI reported 0.1.381. An unauthenticated image
generation request returned 401 before deployment selection. No provider
credential or external inference was used by the smoke checks.

Source `59ced07` adds native Together Image Generation through
`/v1/images/generations`. The adapter maps a bounded output count from one to
four, dimensions, URL or base64 response format, JPEG or PNG output and seed.
It validates the exact ordered output before settlement and permits absent token
usage only for this explicit unit-accounting path, retaining estimated token
provenance while billing the validated image count. Provider protocol, router,
capability-profile, full Go and race tests, vet and build passed. Contract commit
`9497494` publishes the operation and parameter matrix in OpenAPI 0.1.382.

Rancher Desktop built `ai-gateway-gateway:together-image-9497494` with image ID
`sha256:51f514e78eb4ac31c0f9ff8a675f3b8ccfd1457cd7a480b0fa14ae69f28cce73`.
Gateway Helm revision 488 completed successfully. Pod
`ai-gateway-gateway-5555d79f84-6sh6g` became Ready with zero restarts and the
same image ID. Live liveness and readiness returned 204, OpenAPI reported
0.1.382, and an unauthenticated image-generation request returned 401 before
deployment selection. No provider credential or external inference was used by
the smoke checks.

Source `2cef05e` adds model-specific Together Chat reasoning. The adapter
preserves the native `reasoning` field in bounded JSON and SSE responses, maps
unchanged assistant reasoning history back to the provider alias, rejects
conflicting, non-string or oversized values, and admits `reasoning_effort` only
for three exact upstream model IDs with their validated value sets. Provider
capability profiles expose these overrides separately from provider-wide Chat
options. OpenAPI 0.1.383 and its schema regression publish the new profile.
Vet, build, the full Go suite and the full race suite passed.

Rancher Desktop built `ai-gateway-gateway:together-reasoning-2cef05e` with image
ID `sha256:ba896bd8e0e49685c87106f5a8de93c66997dee5dc88293c6ae1cce997ffa8b3`.
Gateway Helm revision 489 completed successfully. Pod
`ai-gateway-gateway-6fb9684b67-d6k4g` became Ready with zero restarts and the
same image ID. Live liveness and readiness returned 204, and the served OpenAPI
reported 0.1.383 with the exact model-specific profile schema. An
unauthenticated Chat request with `reasoning_effort` returned 401 before
deployment selection. No provider credential or external inference was used by
the smoke checks.

Source `07662f1` adds native Together Chat selected-token log probabilities.
The adapter maps public `logprobs=true` to the provider's integer control and
normalizes bounded JSON token arrays and numeric SSE probabilities into the
public response shape. It rejects unsupported top alternatives, malformed
arrays, token identifiers or probabilities, and missing requested probability
data before accepting a completed response. Focused provider tests, vet, build,
the full Go suite and the full race suite passed. OpenAPI 0.1.384 publishes the
updated capability profile.

Rancher Desktop built `ai-gateway-gateway:together-logprobs-07662f1` with image
ID `sha256:5c56cde674ebfbaad0433d63d0b7e63e4a610d42ca3f14e971c4cb64a20e22ed`.
Gateway Helm revision 490 completed successfully. Pod
`ai-gateway-gateway-77d5865559-m2jcw` became Ready with zero restarts and the
same image. Live liveness and readiness returned 204, the served OpenAPI
reported 0.1.384, and an unauthenticated Chat request with `logprobs=true`
returned 401 before deployment selection. No provider credential or external
inference was used by the smoke checks.

Source `b5505d3` enforces the documented `1..32768` output-token range for both
Nemotron 3 model IDs and both public limit names before provider HTTP. Boundary,
alias and no-upstream regressions, OpenAPI validation, vet, build, the full Go
suite and the full race suite passed. OpenAPI 0.1.393 identifies the deployed
contract.

Rancher Desktop built `ai-gateway-gateway:nvidia-output-b5505d3` with image ID
`sha256:c4289a3f03e1f0d857744734512ef883e891df06e5539498fe0a4475c1fda1c8`.
Gateway Helm revision 498 completed successfully. Pod
`ai-gateway-gateway-5d459c568f-knkdk` became Ready with zero restarts. Live
liveness and readiness returned 204 and the served OpenAPI reported 0.1.393.

Source `7cb05d7` prevents replay of `reasoning_content` in NVIDIA NIM DeepSeek
V4 Pro 0813 assistant history. The response trace remains observable, while an
attempt to send it back fails before provider HTTP. Focused provider/API tests,
OpenAPI validation, vet, build, the full Go suite and the full race suite passed.
OpenAPI 0.1.394 identifies the deployed contract.

Rancher Desktop built `ai-gateway-gateway:nvidia-history-7cb05d7` with image ID
`sha256:c0bd2015176eb3a291b8857e8f69069cdc62588cf2a0297e023f3be288809919`.
Gateway Helm revision 499 completed successfully. Pod
`ai-gateway-gateway-7987b99f64-d28jb` became Ready with zero restarts. Live
liveness and readiness returned 204 and the served OpenAPI reported 0.1.394.

Source `40ebc1b` enables the native Together Chat sampling controls already
present in the public request contract. `min_p`, `top_k`,
`repetition_penalty` and integer `logit_bias` now pass through after shared
range and token-ID validation, and the provider capability profile derives the
same support from its execution validator. Protocol tests verify the exact wire
fields and that invalid values make no HTTP request. Focused provider tests,
vet, build, the full Go suite and the full race suite passed. OpenAPI 0.1.385
documents the adapter coverage.

Rancher Desktop built `ai-gateway-gateway:together-sampling-40ebc1b` with image
ID `sha256:b935e087c66cbeee6edd88a711038a28cdd82e72f963762f9654e7640d5bfe78`.
Gateway Helm revision 491 completed successfully. Pod
`ai-gateway-gateway-64c97c6dcf-7pmqg` became Ready with zero restarts and the
same image. Live liveness and readiness returned 204, the served OpenAPI
reported 0.1.385, and an unauthenticated Chat request carrying all four
controls returned 401 before deployment selection. No provider credential or
external inference was used by the smoke checks.

Source `e384b7d` maps public `max_completion_tokens` to Together's native
`max_tokens` field in both JSON and SSE execution. The shared request boundary
continues to reject simultaneous public limit names, and existing TPM and
billing reserve code reads their common effective value. Protocol regressions
verify the exact upstream wire shape for both transports. Focused provider
tests, vet, build, the full Go suite and the full race suite passed. OpenAPI
0.1.386 documents the provider mapping.

Rancher Desktop built `ai-gateway-gateway:together-token-limit-e384b7d` with
image ID `sha256:782d901bd07cd2878e9c8c75923e31bd4a52566ea70c81be3c0d2a04208de14b`.
Gateway Helm revision 492 completed successfully. Pod
`ai-gateway-gateway-56b898c955-rf7t6` became Ready with zero restarts and the
same image. Live liveness and readiness returned 204, the served OpenAPI
reported 0.1.386, and an unauthenticated Chat request carrying
`max_completion_tokens` returned 401 before deployment selection. No provider
credential or external inference was used by the smoke checks.

Source `7765869` normalizes Together's native `eos` Chat terminal reason to the
public `stop` value in JSON and SSE. The adapter preserves every other
documented terminal value, permits `null` only for pending stream chunks, and
rejects missing JSON or unknown terminal reasons before accepting output.
Regressions cover direct normalization and emitted SSE data. Focused provider
tests, vet, build, the full Go suite and the full race suite passed. OpenAPI
0.1.387 identifies the deployed contract revision.

Rancher Desktop built `ai-gateway-gateway:together-finish-7765869` with image
ID `sha256:d904fdedb52cd50922d3bc7d04a633d00eca2d77c380259413fdd12d5251ee40`.
Gateway Helm revision 493 completed successfully. Pod
`ai-gateway-gateway-6765b74b48-rb92h` became Ready with zero restarts and the
same image. Live liveness and readiness returned 204, the served OpenAPI
reported 0.1.387, and an unauthenticated Chat request returned 401 before
deployment selection. No provider credential or external inference was used by
the smoke checks.

Source `9caf413` adds model-specific Together hybrid reasoning controls. For
nine documented hybrid model IDs, public `reasoning_effort=none` maps to native
`reasoning.enabled=false`; the current DeepSeek V4 Pro ID additionally accepts
`high` and `max`. Adjustable GPT-OSS and the prior versioned DeepSeek policies
remain distinct. Reasoning output and unchanged assistant history use the
native alias only for the explicit model list, and the capability profile
publishes the exact per-model values. Focused provider tests, vet, build, the
full Go suite and the full race suite passed. OpenAPI 0.1.388 identifies the
deployed contract revision.

Rancher Desktop built
`ai-gateway-gateway:together-hybrid-reasoning-9caf413` with image ID
`sha256:a91b98cb925425277d1d75fd33485bf0684700196e0b7644cc07a1d72045f3b0`.
Gateway Helm revision 494 completed successfully. Pod
`ai-gateway-gateway-96595fd5f-ttjcc` became Ready with zero restarts and the
same image. Live liveness and readiness returned 204, the served OpenAPI
reported 0.1.388, and an unauthenticated hybrid-reasoning request returned 401
before deployment selection. No provider credential or external inference was
used by the smoke checks.

Source `c91df49` bounds NVIDIA NIM Chat reasoning controls by exact model ID.
Nemotron 3 Super accepts `none`, `low`, and `high`; Nemotron 3 Ultra accepts
`none`, `medium`, and `high`. Other model/value combinations fail before HTTP,
and capability discovery reports the two model overrides without advertising a
provider-wide default. Focused provider/API tests, vet, build, the full Go suite
and the full race suite passed. OpenAPI 0.1.389 identifies the deployed contract
revision.

Rancher Desktop built `ai-gateway-gateway:nvidia-reasoning-c91df49` with image
ID `sha256:0a6986d21354d501b467a9169314c72cd51313953aaed63c9e394ccbe76983ba`.
Gateway Helm revision 495 completed successfully. Pod
`ai-gateway-gateway-659bc9b8d4-ljvnv` became Ready with zero restarts and the
same image. Live liveness and readiness returned 204 through the ingress IP,
the served OpenAPI reported 0.1.389, and an unauthenticated Chat request using
the Ultra reasoning control returned 401 before deployment selection. No
provider credential or external inference was used by the smoke checks.

Source `0195478` extends the NVIDIA NIM model policy with the exact
`deepseek-ai/DeepSeek-V4-Pro-0813` reasoning values `low`, `high`, and `max`.
Source `e888f1f` preserves NIM's top-level reasoning-token counter as public
completion-token details for JSON and SSE. It rejects null, negative,
fractional, conflicting and over-total values before downstream accounting.
Focused provider/API tests, vet, build, the full Go suite and the full race
suite passed. OpenAPI 0.1.391 identifies the combined deployed contract.

Rancher Desktop built
`ai-gateway-gateway:nvidia-reasoning-usage-e888f1f` with image ID
`sha256:5716daf7a79c7d55564405acaaba4650587000571ab3f34f7d81bf7eece415c4`.
Gateway Helm revision 496 completed successfully. Pod
`ai-gateway-gateway-748588f86b-rlk8q` became Ready with zero restarts on that
image. Live liveness and readiness returned 204, the served OpenAPI reported
0.1.391, and an unauthenticated DeepSeek reasoning request returned 401 before
deployment selection. No provider credential or external inference was used by
the smoke checks.

Source `ebd6e9c` maps public `max_completion_tokens` to NVIDIA NIM's native
`max_tokens` field for JSON and SSE. The shared request boundary still rejects
both public limit names together, and TPM plus billing reserve continue to use
their common effective output cap. Exact-wire provider regressions, OpenAPI
validation, vet, build, the full Go suite and the full race suite passed.
OpenAPI 0.1.392 documents the mapping.

Rancher Desktop built `ai-gateway-gateway:nvidia-token-limit-ebd6e9c` with
image ID `sha256:84e537cbfb46b5347e8dc4d5918ddf9a92a704d81a0fa278aceed2ca70d069dc`.
Gateway Helm revision 497 completed successfully. Pod
`ai-gateway-gateway-55f877b94c-r8xwz` became Ready with zero restarts on that
image. Live liveness and readiness returned 204, the served OpenAPI reported
0.1.392, and an unauthenticated request carrying `max_completion_tokens`
returned 401 before deployment selection. No provider credential or external
inference was used by the smoke checks.

Source `21685e3` enforces the documented maximum temperature of 1 for both
Nemotron 3 model IDs before provider HTTP. The boundary value remains valid and
other NIM model policies are unchanged. Focused provider/API tests, OpenAPI
validation, vet, build, the full Go suite and the full race suite passed.

Rancher Desktop built `ai-gateway-gateway:nvidia-temperature-21685e3` with image
ID `sha256:517de0b55f597f4a1e7b67f65082de2b232586de310ba84a7f85105728e1ca11`.
Gateway Helm revision 500 completed successfully. Pod
`ai-gateway-gateway-75fb6844b6-w5h2n` became Ready with zero restarts. Live
liveness and readiness returned 204 and OpenAPI 0.1.395 was served.

Source `a966baa` rejects negative seeds for both Nemotron 3 model IDs before
provider HTTP. Seed zero remains valid and unrelated NIM model policies remain
unchanged. Focused provider/API tests, OpenAPI validation, vet, build, the full
Go suite and the full race suite passed.

Rancher Desktop built `ai-gateway-gateway:nvidia-seed-a966baa` with image ID
`sha256:b89fe2e0885dd965e502b8f00b97ad740383ea4f554e658eebaabbcd48b761ae`.
Gateway Helm revision 501 completed successfully. Pod
`ai-gateway-gateway-98ccf85c-f2v7l` became Ready with zero restarts on that
image. Direct pod liveness and readiness returned 204, OpenAPI 0.1.396 was
served, and an unauthenticated negative-seed request returned 401 at the auth
boundary without provider execution.

Source `078351e` replaces ASCII word boundaries in Cyrillic anonymizer rules
with explicit Unicode boundaries, preserves labels through capture groups and
supports case-insensitive excluded capture values for known dialogue
confirmations. Regression tests cover dialogue code words, Russian password
labels, single Cyrillic names, month names and excluded values. Vet, build, the
full Go suite, the race suite and Helm lint passed.

Rancher Desktop built `ai-gateway-anonymizer:unicode-078351e` with image ID
`sha256:ad78580cff6800eadf89a9651d056545c74c88d836f36756812232e84729931c`.
Anonymizer Helm revision 4 completed successfully. Pod
`ai-gateway-anonymizer-86d77447c5-p9czq` became Ready with zero restarts. A
cluster-local synthetic request verified masking for the dialogue code word,
Russian password and login labels, security key, internal identifiers, broker
account and Cyrillic month date while preserving the confirmation word and
surrounding punctuation.

Source `d319443` adds composable anonymization profiles and policy attachment
scopes for organization, team, user, credential, model, provider, deployment
and tags. Provider/deployment filters are resolved independently for every
fallback attempt. Effective policy metadata survives batch and durable
background execution and participates in cache isolation. Logs contain only
profile/rule names and replacement counts. The admin API publishes the active
rule inventory, and the Guardrails/Policies UI supports custom rule selection,
coverage inspection, policy simulation and anonymized compliance previews.
Gateway and anonymizer vet, build, full test and race suites passed; all 167 UI
tests and UI type checking passed.

Rancher Desktop built `ai-gateway-gateway:anon-policy-5037067` with image ID
`sha256:d99ad682912d875183fb0286b6fa09dff41b1b834bf5f9462578e14a4b2af46c`
and `ai-gateway-anonymizer:anon-policy-5037067` with image ID
`sha256:19e32ea52889a50fb49c1e2e489782b004011bd8f548231a66db372b3c37e24c`.
Gateway Helm revision 502 and anonymizer revision 5 completed successfully;
both pods became Ready with zero restarts. Liveness and readiness returned 204,
the admin inventory returned 40 active rules, and a live custom-profile check
masked an email and a Russian passport into two placeholders without returning
the source values and reported `content_stored=false`. The temporary test policy
was restored after verification.

Source `f0094dd` adds native GenerateContent `serviceTier` and `store`
controls, preserves the effective service tier in JSON and SSE usage, and
bypasses exact and semantic response caches when provider-side storage is
requested. Invalid tiers fail before provider execution. Native conversion,
exact Gemini wire, capability-profile, cache-bypass and OpenAPI regressions,
vet, build, the full Go suite and the full race suite passed.

Rancher Desktop built `ai-gateway-gateway:gemini-controls-f0094dd` with image
ID `sha256:fce33acd3abcf3297e87ffa0550e2fe810c96d5a3a4dbf6fbaf4fbfb6e601b04`.
Gateway Helm revision 503 completed successfully. Pod
`ai-gateway-gateway-59f66ff795-dl6gx` became Ready with zero restarts. Live
liveness and readiness returned 204, OpenAPI 0.1.397 was served, and an invalid
native service tier returned 400 before authentication or provider execution.

Source `92be558` includes provider-reported Gemini
`toolUsePromptTokenCount` in billable input usage while retaining the native
prompt/tool split in GenerateContent JSON and SSE responses. Shared validation
rejects negative, overflowing and internally inconsistent tool-token totals for
Chat, image, audio and OCR GenerateContent consumers. Focused provider,
gateway, billing and OpenAPI regressions, vet, build, the full Go suite and the
full race suite passed.

Rancher Desktop built `ai-gateway-gateway:gemini-usage-92be558` with image ID
`sha256:8474edc4331f66c80fb54e1300e2bc79f7ebe93e70d98fc4e722b32307b88a13`.
Gateway Helm revision 504 completed successfully. Pod
`ai-gateway-gateway-7f8c649999-tsh69` became Ready with zero restarts. Live
liveness and readiness returned 204 and OpenAPI 0.1.398 was served.

Source `059beac` adds native GenerateContent URL Context with separate
deployment capability and tool authorization, bounded provider metadata,
tool-declaration TPM reserve, provider-reported tool-input billing and exact
and semantic cache bypass. Native token counting applies the same tool grant.
The deployment and catalog selectors expose URL Context in the UI, while both
URL Context and managed code execution require explicit endpoint capabilities.
Focused regressions, OpenAPI validation, vet, build, the full Go suite, the
full race suite, all 167 UI tests and UI type checking passed.

Rancher Desktop built
`ai-gateway-gateway:gemini-url-context-059beac` with image ID
`sha256:83c64864088fd215e751e70841c8a2be924b2b55894e6ebfd87fa69df72c42b4`.
Gateway Helm revision 505 completed successfully. Pod
`ai-gateway-gateway-869c84bb54-tcz99` became Ready with zero restarts on that
image. Live liveness and readiness returned 204, OpenAPI 0.1.399 exposed the
URL Context request, response and capability contract, the production UI bundle
contained both managed Gemini tool selectors, and an unauthenticated URL Context
request returned 401 at the authentication boundary without provider execution.

Source `c9cf8ca` adds native GenerateContent Google Maps grounding with a
separate deployment capability and tool authorization, optional validated
coordinates, bounded place/review metadata, URL citations, actual grounding-use
billing and exact and semantic cache bypass. Native token counting applies the
same tool grant and includes the tool configuration in the TPM estimate.
Focused regressions, OpenAPI validation, vet, build, the full Go suite, the full
race suite, all 167 UI tests and UI type checking passed.

Rancher Desktop built `ai-gateway-gateway:gemini-maps-c9cf8ca` with image ID
`sha256:48486c4a00e9c4c5b33026706877e1e5732f0bf7bd130f71cce70710737d466e`.
Gateway Helm revision 506 completed successfully. Pod
`ai-gateway-gateway-6c9f9fd65b-dcc7s` became Ready with zero restarts on that
image. Live liveness and readiness returned 204, OpenAPI 0.1.400 exposed the
Maps request and capability contract, the production UI bundle contained the
Maps capability selector, and an unauthenticated Maps request returned 401 at
the authentication boundary without provider execution.

Source `25677d3` extends native GenerateContent inline and owner-scoped stored
video input to MPEG, MPG, MOV, AVI, FLV, WMV and 3GPP alongside MP4 and WebM.
Every declared MIME type must match its bounded container or stream signature;
the content then uses the existing AV projection, TPM reserve, `video_input`
capability routing and response-cache exclusion. Focused provider, gateway,
OpenAPI and shared-media regressions, vet, build, the full Go suite and the full
race suite passed.

Rancher Desktop built `ai-gateway-gateway:gemini-video-25677d3` with image ID
`sha256:0114fd5f9f0e1a10ec635ab19e63118dce7db4b9cbb14791b8710be7b9b3e238`.
Gateway Helm revision 507 completed successfully. Pod
`ai-gateway-gateway-ff5b8bd89-qlfjf` became Ready with zero restarts on that
image. Live liveness and readiness returned 204, OpenAPI 0.1.401 exposed the
expanded MIME contract, and a cluster-local unauthenticated AVI request returned
401 at the authentication boundary without provider execution.

## Native Gemini file search

Native GenerateContent and countTokens accept bounded provider-managed file-search
store configuration. Every store is authorized separately, tool schemas enter token
reserve, provider token counts remain authoritative for settlement, and requests
bypass response caches. Gemini and Vertex routing requires an explicit capability.
Retrieval grounding metadata is validated and preserved, while file-search execution
does not create a synthetic search-request charge.

## Native Gemini computer use

Native GenerateContent accepts bounded browser, mobile and desktop computer-use
configuration. Deployment capability, environment grants and per-policy safety
override grants fail closed before provider execution. Tool configuration participates
in TPM and budget reserve, countTokens preserves it, and response caches are bypassed.
The gateway transports action calls and safety acknowledgements; execution remains
client-side.

## Native Gemini MCP execution

Remote MCP execution is registry-backed and fail closed. Requests carry only server IDs; the gateway resolves URLs and bearer credentials after authentication, requires an explicit per-server provider-execution opt-in and connector grant, and accepts Streamable HTTP only. Resolved requests bypass response caches and retain provider token accounting through native count and inference paths.

## Vector-store chunking persistence

Vector-store attachments persist their effective `auto` or `static` chunking
strategy in PostgreSQL. Single-file attachment, atomic file batches, and RAG
ingestion write the same representation. Content retrieval and synchronous
search read that stored strategy after a restart, preventing chunk boundaries
from changing because request-local state was lost. The database constraint
rejects static sizes outside 100 to 4096 estimated tokens and overlap above half
the configured chunk size.

## Background Responses conversations

Background Responses can use the durable Conversations API. Current input items
are staged in a separate PostgreSQL table before the background job is exposed;
the queue payload retains identifiers and effective policy metadata but no prompt.
A durable active-turn marker prevents lease expiry from admitting a second request
or CRUD mutation while provider execution is pending. Terminal success atomically
commits staged input and provider output, while failed and cancelled responses
remove pending input and release the turn. Completion and release are idempotent by
the internal execution ID so worker retries cannot duplicate conversation history.
Admission reserves the bounded maximum of 1024 provider output items against the
conversation quota before execution, preventing an unrecoverable terminal quota
failure from retaining the durable turn.

## Direct guardrail anonymization

Source `3ce760a` applies the effective policy's configured anonymizer to
`POST /guardrails/apply_guardrail`, including policies that enable only
anonymization. Every enabled DLP, AV or anonymizer module is required and fails
closed when unavailable. The response reports the replacement count and includes
masked text only when at least one replacement occurred; raw input is never echoed
and durable audit remains metadata-only. Gateway unit tests, vet, build, the full
Go suite and the full race suite passed.

Rancher Desktop built
`ai-gateway-gateway:guardrail-anonymization-3ce760a3` with image ID
`sha256:a2300173ae6bc7b21aa01d292548fe871c41653785631167f9f1691b314ac260`.
Gateway Helm revision 582 completed successfully. Pod
`ai-gateway-gateway-75f7b78f46-l457b` became Ready with zero restarts. Live
liveness and readiness returned 204, OpenAPI 0.1.463 was served, and a direct
request using the configured `test` policy returned one replacement as
`{{EMAIL_1}}` without raw content or stored content.

## Managed OpenAI fast service tiers

Source `1021b1e` accepts and forwards the documented `fast` and `ultrafast`
service tiers for managed OpenAI Chat and Responses requests. The runtime-derived
capability profile publishes both values, while unsupported tiers still fail before
provider execution. Transport regressions cover both endpoints and exact forwarded
values. Provider and OpenAPI tests, vet, build, the full Go suite and the full race
suite passed.

Rancher Desktop built `ai-gateway-gateway:service-tiers-1021b1e2` with image ID
`sha256:9c36e926ca0fa3d1fe699526979dc7d3c64d553d5f638bd6609fe276a2011b79`.
Gateway Helm revision 583 completed successfully. Pod
`ai-gateway-gateway-c647c5b87-kmlf2` became Ready with zero restarts. Live
liveness and readiness returned 204, OpenAPI 0.1.464 was served, and the live
provider-capability endpoint reported `fast` and `ultrafast` for managed OpenAI
Chat and Responses without executing model inference.

## Native Groq service-tier request contract

Source `4cbff5f` limits native Groq Chat requests to the documented input values
`auto`, `on_demand`, `flex` and `performance`. The provider-reported `default`
value remains valid in responses and in the separate Responses API request
contract, but Chat now rejects it before provider execution. The runtime-derived
capability profile and OpenAPI description publish the same distinction. Provider
and OpenAPI tests, vet, build and the full race suite passed.

Rancher Desktop built `ai-gateway-gateway:groq-tiers-4cbff5fb` with image ID
`sha256:8c0b134ee567acdec84c1c44a4dae9a97077923d701117795ba7c6e99245a071`.
Gateway Helm revision 584 completed successfully. Pod
`ai-gateway-gateway-56c4f54c77-6957p` became Ready with zero restarts. Live
liveness and readiness returned 204, OpenAPI 0.1.465 was served, and the live
provider-capability endpoint reported the four Chat request tiers while retaining
`auto`, `default` and `flex` for Responses.

## Native Cerebras effective service tier

Source `c49c167` normalizes Cerebras `service_tier_used` into the public
`service_tier` response field when automatic tier selection is requested. JSON
and SSE now expose the tier that actually processed the request; the native-only
field is removed from streamed payloads, and unknown, malformed or conflicting
provider values fail closed. Provider and OpenAPI tests, vet, build and the full
race suite passed.

Rancher Desktop built `ai-gateway-gateway:cerebras-tier-c49c167f` with image ID
`sha256:c6daa89321b22863738db582c03df1ac02b65f170035372dbca9102f1b5e411b`.
Gateway Helm revision 585 completed successfully. Pod
`ai-gateway-gateway-84c6857d9c-ms4vc` became Ready with zero restarts. Live
liveness and readiness returned 204, OpenAPI 0.1.466 was served, and the live
provider-capability endpoint retained the validated Cerebras request tiers.

## Provider-reported Chat service tiers

Source `f9c1be4` validates provider-reported Chat service tiers before JSON
delivery or the first SSE callback. The response allowlist covers the request
values plus the provider-assigned `standard` and `batch` values. Unknown values
now fail closed instead of entering usage and billing metadata. OpenAI contract,
provider and API tests, vet, build and the full race suite passed.

Rancher Desktop built `ai-gateway-gateway:reported-tiers-f9c1be4f` with image ID
`sha256:4b1dda00c028fa1677e4d4b4ff3b953938e2a4e128ce767ee8cf18106cb6d62a`.
Gateway Helm revision 586 completed successfully. Pod
`ai-gateway-gateway-5c76bf8b65-7sjrj` became Ready with zero restarts. Live
liveness and readiness returned 204 and OpenAPI 0.1.467 was served.

## Runtime model-policy schema and Cerebras thinking history

Source `35d033a` removes the stale closed enum from the OpenAPI model-specific
Chat policy schema. Runtime adapters can now publish exact upstream model IDs
without producing an admin response that violates the gateway's own schema.
The schema keeps a non-empty model constraint and its regression test.

Source `d38eaba` adds the native Cerebras `clear_thinking` request control for
the exact upstream model `zai-glm-4.7`. Explicit `false` is preserved on the
wire, other Cerebras models and adapters reject the field before provider
execution, and the value participates in exact and semantic cache identity.
The runtime capability profile advertises the option only in that model's
override. Provider and API tests, vet, build and the full race suite passed.

Rancher Desktop built `ai-gateway-gateway:cerebras-clear-d38eaba8` with image
ID `sha256:912a358ecd541c8aed912cca1d58b4b263bec0796c8bc3e9dbf2179ce4400622`.
Gateway Helm revision 587 completed successfully. Pod
`ai-gateway-gateway-6fc784dffc-f85d8` became Ready with zero restarts. Live
liveness and readiness returned 204, OpenAPI 0.1.469 was served, and the live
Cerebras capability profile reported `clear_thinking` only for `zai-glm-4.7`.

## Chat reasoning policy coverage

Source `9a84d79` includes unsigned Chat `reasoning_content` in input and output
DLP projections, masks the field before provider execution and restores its
request-local placeholders in successful responses. Signed reasoning blocks
remain immutable so their signatures stay valid, while their readable thinking
text remains covered by DLP. Regression tests cover input projection, output
projection, masking, restoration and preservation of signed blocks. Gateway
tests, vet, build and the full race suite passed.

Rancher Desktop built `ai-gateway-gateway:reasoning-policy-9a84d799` with image
ID `sha256:fe5cb57869a63f89fe97c3142c5003662e2720ef9e75dc98fe6832c6ca35f3ea`.
Gateway Helm revision 588 completed successfully. Pod
`ai-gateway-gateway-86995544c4-nlsb5` became Ready with zero restarts. Live
liveness and readiness returned 204 and OpenAPI 0.1.470 was served.

## Native Groq reasoning output controls

Source `43656e9` adds the mutually exclusive Chat controls
`include_reasoning` and `reasoning_format`, validates the latter as `hidden`,
`raw` or `parsed`, and preserves explicit false values on the native wire.
Groq JSON and SSE reasoning strings are bounded and normalized to public
`reasoning_content`; conflicting or malformed aliases fail closed. The controls
participate in exact and semantic cache identity, and other adapters reject them
before provider execution. Runtime capabilities expose the supported formats.
OpenAI contract, adapter-isolation, capability, cache, OpenAPI, JSON and SSE
regressions passed together with vet, build and the full race suite.

Rancher Desktop built `ai-gateway-gateway:groq-reasoning-43656e9a` with image
ID `sha256:92edabf53fc5d9d79b2cdb8641aa5c81246f2cd576972bd71827f33c87e13143`.
Gateway Helm revision 589 completed successfully. Pod
`ai-gateway-gateway-67ffcc555c-thqxg` became Ready with zero restarts. Live
liveness and readiness returned 204, OpenAPI 0.1.471 was served, and the live
Groq capability profile reported `include_reasoning` plus the three validated
formats while every other provider profile omitted both controls.

## Model-scoped Groq reasoning policy

Source `4638192` replaces the provider-wide Groq reasoning claims with exact
model policies. `openai/gpt-oss-20b` and `openai/gpt-oss-120b` accept
`include_reasoning` plus `low`, `medium` and `high` effort. The exact
`qwen/qwen3.8-27b` model accepts `none`, `default`, `low`, `medium` and `high`
effort plus `hidden`, `raw` and `parsed` formats. Raw format is rejected with
tools or a JSON response format, and all reasoning controls are rejected for
unlisted models before provider execution. Capability, protocol, OpenAPI,
adapter and full race regressions passed together with vet and build.

Rancher Desktop built `ai-gateway-gateway:groq-model-policy-46381923` with
image ID `sha256:00bf3e94a1e4b991a120f19f13163350d42cb51660b136a61f292f5e775e019e`.
Gateway Helm revision 590 completed successfully. Pod
`ai-gateway-gateway-5dfdbc5448-vl7cv` became Ready with zero restarts. Live
liveness and readiness returned 204, OpenAPI 0.1.472 was served, the Groq
provider-wide reasoning arrays were empty, and all three exact model policies
matched the runtime validator.

## Native Groq citation control

Source `dc59c7f` adds the validated `citation_options` Chat control with the
exact `enabled` and `disabled` values. The native Groq adapter preserves the
value on the provider request; every other adapter rejects it before provider
execution. The option participates in exact and semantic cache identity and is
published by the runtime capability profile. Contract, adapter-isolation,
capability, cache and OpenAPI regressions passed together with vet, build and
the full race suite.

Rancher Desktop built `ai-gateway-gateway:groq-citations-dc59c7f2` with image
ID `sha256:a8196c52f3aed8a4e75ad0a0abd3ddfdda93e3644ee809c0346f7882d4cf6c0c`.
Gateway Helm revision 591 completed successfully. Pod
`ai-gateway-gateway-75d4d67557-fhbxf` became Ready with zero restarts. Direct
service checks returned 204 for liveness and readiness, OpenAPI 0.1.473 was
served, the live Groq profile reported both citation values, other inspected
provider profiles omitted them, and the three exact Groq reasoning policies
remained intact.

## Native DeepSeek thinking controls

Source `0ebec2c` exposes the native Chat `thinking.type` switch and validated
reasoning effort while preserving the gateway's prior non-thinking default when
both controls are omitted. Conflicting switch and effort values, temperature,
out-of-range thinking-mode nucleus sampling, and required or named tool choices
fail before provider execution. The controls are isolated from other adapters,
participate in exact and semantic cache identity, and appear in the runtime
capability profile. Contract, wire, rejection, adapter, cache, capability and
OpenAPI regressions passed together with vet, build and the full race suite.

Rancher Desktop built `ai-gateway-gateway:deepseek-thinking-0ebec2cd` with image
ID `sha256:43f39c783d62bb9e377189ab5e910b18ad43a5e9fc96fb46ff84d7b1175a7f66`.
Gateway Helm revision 592 completed successfully. Pod
`ai-gateway-gateway-5ddf9c469f-2m22d` became Ready with zero restarts. Direct
service checks returned 204 for liveness and readiness, OpenAPI 0.1.474 was
served, the live DeepSeek profile reported both thinking values and seven
validated effort values, and the Groq controls remained isolated.
