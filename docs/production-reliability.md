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
