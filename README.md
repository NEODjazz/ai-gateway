# AI Gateway

A fast, modular AI gateway written in Go with OpenAI API compatibility.

## Overview

The gateway accepts requests in the OpenAI API format, runs gateway-level functions, and forwards each request to the provider router. Provider-level functions run as part of a specific attempt to call an AI provider endpoint. Each function can be required or optional:

- `required: true` — the request stops if the function is unavailable or returns an error.
- `required: false` — the request continues without the function if it is unavailable.

The current processing levels are:

- Gateway level: `auth`.
- Provider level: `anonymizer`, `dlp`, `av`, and `billing`.

## Microservices

- `gateway` — the central request entry point, OpenAI-compatible API, routing, and function pipeline.
- `anonymizer` — replaces sensitive information with request-scoped placeholders.
- `auth` — authorization, API keys, JWTs, roles, and permissions.
- `billing` — tracks usage, tokens, and costs by user, key, model, and provider.
- `dlp` — scans content for data leaks through ICAP.
- `av` — scans content for malware through ICAP.

## Quick start

```powershell
cd repos/gateway
go run ./cmd/gateway
```

Test the gateway:

```powershell
Invoke-RestMethod -Method Post http://localhost:8080/v1/chat/completions `
  -Headers @{ Authorization = "Bearer demo-admin-key"; "Content-Type" = "application/json" } `
  -Body '{"model":"demo-model","messages":[{"role":"user","content":"hello from user@example.com"}]}'
```

## Run in Rancher Desktop with Helm

Each service is installed as a separate Helm chart:

- `charts/ai-gateway` — gateway only.
- `charts/auth` — authorization.
- `charts/anonymizer` — anonymization.
- `charts/billing` — billing.
- `charts/security` — DLP and antivirus.
- `charts/redis`, `charts/postgres`, and `charts/clickhouse` — data stores.

```powershell
kubectl create namespace ai-gateway --dry-run=client -o yaml | kubectl apply -f -
helm upgrade --install ai-gateway-redis charts/redis --namespace ai-gateway
helm upgrade --install ai-gateway-postgres charts/postgres --namespace ai-gateway
helm upgrade --install ai-gateway-clickhouse charts/clickhouse --namespace ai-gateway
helm upgrade --install ai-gateway-auth charts/auth --namespace ai-gateway
helm upgrade --install ai-gateway-anonymizer charts/anonymizer --namespace ai-gateway
helm upgrade --install ai-gateway-billing charts/billing --namespace ai-gateway
helm upgrade --install ai-gateway-security charts/security --namespace ai-gateway
helm upgrade --install ai-gateway charts/ai-gateway --namespace ai-gateway
kubectl -n ai-gateway get pods
```

The release names in this example match the module URLs in `charts/ai-gateway/values.yaml`. If you use different release names, set `gateway.modules.<module>.url` explicitly.

When migrating from the previous combined release, upgrade `ai-gateway` first so Helm can remove the old module Deployments and Services, then install the new service releases. Otherwise, Helm cannot adopt existing resources owned by another release.

Production mode is enabled by default and uses the published service images from `ghcr.io/neodjazz`. To run services directly from the current project directory using the pinned `golang:1.26.7-alpine` toolchain, enable `devMode` explicitly with `--set devMode.enabled=true` for each application chart. The module language baseline remains Go 1.25, while production and development builds use the patched Go 1.26 toolchain.

### Ollama provider

In Rancher Desktop, the chart connects to a local Ollama instance on the Windows host by default and includes a demo fallback:

```text
DEFAULT_PROVIDER=ollama
PROVIDERS_JSON=[...]
```

Set the model with the `model` field. You can select a provider with the extended `provider` field. If `provider` is omitted, the gateway uses `DEFAULT_PROVIDER`. If `provider` is set to `"auto"`, the gateway selects the first suitable endpoint based on model compatibility and priority.

```powershell
Invoke-RestMethod -Method Get http://127.0.0.1:11434/api/tags
```

Example request through the gateway:

```powershell
Invoke-RestMethod -Method Post http://127.0.0.1:18080/v1/chat/completions `
  -Headers @{ Authorization = "Bearer demo-admin-key"; "Content-Type" = "application/json" } `
  -Body '{"provider":"ollama","model":"lfm2.5-thinking:1.2b","messages":[{"role":"user","content":"Reply with exactly this text and nothing else: user@example.com"}]}'
```

The Responses API is also supported:

```powershell
Invoke-RestMethod -Method Post http://127.0.0.1:18080/v1/responses `
  -Headers @{ Authorization = "Bearer demo-admin-key"; "Content-Type" = "application/json" } `
  -Body '{"provider":"ollama","model":"lfm2.5-thinking:1.2b","input":"Reply with exactly this text and nothing else: user@example.com"}'
```

For `/v1/responses`, the gateway uses the same flow: gateway-level authentication, provider routing and failover, provider-level anonymization, DLP, antivirus, and billing, followed by response deanonymization. The contract supports text input/output, function tools and tool choice, structured `text.format`, `previous_response_id`, and streamed function-call argument events.

`POST /v1/embeddings` supports a string or an array of strings and runs through
the same authentication, model grants, rate limits, DLP/AV, anonymization,
provider failover, and reserve/commit/cancel billing lifecycle. OpenAI-compatible
providers use `/v1/embeddings`; Ollama uses its native `/api/embed`. Only
`encoding_format: "float"` is accepted. Token-ID arrays are deliberately rejected
because DLP/AV cannot inspect them safely without the tokenizer for the selected
model.

`phi3` and most chat-only Ollama models return `501 Not Implemented` from
`/api/embed`. For a positive local smoke test, install a dedicated embedding
model and enable the disabled `ollama-embeddings` endpoint from the chart
values:

```powershell
ollama pull nomic-embed-text
```

Copy `charts/ai-gateway/values.yaml` into the environment-specific values file,
set `enabled: true` on the endpoint named `ollama-embeddings`, and upgrade the
release with that file. When overriding the complete provider array, keep the
embedding endpoint scoped to `capabilities: [embeddings]`, model
`nomic-embed-text:latest`, and base URL
`http://host.docker.internal:11434`. Then run the validating smoke test (it
requires `curl` and `jq`):

```bash
./scripts/smoke-ollama-embeddings.sh
```

The script sends two inputs and fails unless the gateway returns two non-empty
float vectors, stable indexes, and non-zero usage.

`POST /v1/rerank` implements the LiteLLM/Cohere-style contract with `query`,
`documents`, optional `top_n`, `rank_fields`, and `return_documents`. Rerank is
an explicit capability: configure a dedicated OpenAI-compatible endpoint with
`capabilities: [rerank]`. The adapter calls `/v1/rerank` by default; for vLLM or
TEI deployments exposing the root path, set `rerank_path: /rerank`. The request
uses the same authentication, model ACL, rate limits, DLP/AV text projection,
anonymization, admission control, failover, billing, and optional shadow
mirroring as other inference APIs. When documents are returned, the gateway
restores the original document selected by the provider index, rather than
exposing an anonymized projection.

```bash
curl -sS http://127.0.0.1:18080/v1/rerank \
  -H 'Authorization: Bearer demo-admin-key' \
  -H 'Content-Type: application/json' \
  -d '{"model":"rerank-model","query":"refund policy","documents":["shipping terms","refunds within 30 days"],"top_n":1,"return_documents":true}'
```

`/v1/chat/completions` forwards OpenAI function tools, tool choice, parallel-tool policy, stop/seed, and JSON object or JSON Schema response formats. Anthropic tool definitions, calls, results, and forced structured outputs are translated to and from its native content blocks; Ollama receives its native `tools` and `format` fields. Tool arguments are included in the DLP/AV text projection and anonymized independently of the tool schema.

### OpenAPI contract

The canonical external contract is [repos/gateway/api/openapi.yaml](repos/gateway/api/openapi.yaml).

### Admin UI

The gateway serves a self-contained route-based React/TypeScript operations
console at `/ui/`. Its stable dashboard routes are organized into Monitor,
Manage, AI Hub, Govern, and System workspaces. Shared API/auth, resource table,
form, loading, empty, conflict, and error components keep CRUD behavior
consistent; unsupported backend capabilities are marked unavailable instead of
showing simulated data. Deep links such as `/ui/providers` are served by the
embedded SPA without a CDN. Frontend sources, Vitest component tests, and the
Vite production build live in `repos/gateway/ui`; generated assets are embedded
in the Go binary. It provides
an overview, virtual-key lifecycle management, model and runtime-catalog
management, budget policy management, and a read-only management audit view.
The Providers & Models workspace adds independent provider endpoint CRUD,
AES-GCM encrypted write-only credentials, runtime deployment CRUD, public model
groups, priority fallback, weighted/adaptive routing, connection tests, and
provider model discovery. The Providers route joins non-secret credential and
deployment counts, lets the operator select a matching stored credential for a
connection test or discovery, and passes that selection into Model Onboarding.
The backend rejects credentials bound to a different provider. Probe results
contain only status, latency and model count; discovery returns model identifiers
without exposing the selected secret. A credential secret is accepted only by
credential write operations and is never returned; the UI supplies it only for
creation or explicit rotation. List and mutation responses contain metadata only. The
Credentials route separates metadata editing from an audited secret-rotation
operation, shows non-secret provider/deployment relationships, and never
repopulates secret fields. Metadata-only updates preserve the encrypted value.
Provider-bound credentials can be used only by matching provider probes and
deployments; shared credentials remain available as an explicit compatibility
option.
The guided Model Onboarding route performs a server-side dry run and an
optimistic atomic apply, so catalog, deployment, and model-group changes cannot
be partially published. Set a stable
`PROVIDER_CREDENTIAL_ENCRYPTION_KEY` in managed environments. Without it, the
gateway generates an ephemeral process key suitable only for local runtime
management. Set `PROVIDER_CONTROL_PLANE_POSTGRES_DSN` to persist providers,
encrypted credentials, deployments, model groups, guardrail policies and their
scope attachments, managed tag policies, projects/access groups, MCP servers/toolsets, agent/tool-policy templates, and
logging destinations as one versioned JSONB snapshot. Logging bearer secrets
use domain-separated AES-GCM encryption and are never returned by the API. The
dedicated Logging & Alerts console exposes queue/delivery/failure/drop counters,
configured callback metadata and auditable fixed-content probes. Editing with
an empty secret preserves the encrypted value; probes never contain prompts or
responses and fail closed when the audit preflight is unavailable. The
gateway seeds an empty store from `PROVIDERS_JSON`, then treats
PostgreSQL as the source of truth. The runtime model catalog is stored in the
same snapshot; an existing Redis catalog is imported once when a legacy
installation first starts with control-plane persistence. Every mutation uses an optimistic revision,
is committed before the API reports success, and is rolled back in memory when
persistence fails. Replicas poll the durable revision (one second by default),
while Redis carries the same revision marker for cross-replica observability.
Startup fails if PostgreSQL is unavailable, the persisted snapshot is invalid,
or the stable encryption key cannot decrypt a credential or logging secret.
The Usage & Spend view reads final request outcomes from ClickHouse for a
bounded 7/30/90-day window and breaks requests, tokens, latency, and spend down
by day, public model, concrete upstream model, logical provider, routed endpoint,
and virtual-key tag. Public aliases are not merged with provider model names, and
provider totals are not confused with deployment/endpoint totals. Every tagged request is attributed
to each of its tags; untagged traffic is grouped as `Untagged`. Costs remain
separated by currency, and model/provider/tag filters use typed ClickHouse
parameters. Every dimension row in the UI can open a server-filtered drill-down
for the active period; upstream model, endpoint, virtual-key fingerprint, user,
team, and organization filters are also parameterized and bounded.
Provider-reported prompt-cache usage is retained separately as cache-read and
cache-write input tokens in Usage and Logs; these counters do not replace total
tokens and are not inferred from gateway cache hits.
The Request Logs view provides a bounded, cursor-paginated explorer over the
same final outcomes with filters for request, session, OpenTelemetry trace,
status, model, endpoint, tag, user, team, organization, cache outcome, and credential
fingerprint over bounded 7/30/90-day windows. Requests can also be grouped by
session or distributed trace; an explicit RFC3339 custom window up to 90 days is
available for incident investigations, and group spend remains separated by currency. Its
detail contract contains operational
metadata, token counts (including provider-reported cache read/write input
tokens), cost, cache state, and a bounded failure class only.
The selected request/audit tab, request view, time window, applied filters, and
request-detail ID are represented in the `/ui/logs` query string so diagnostic
views survive refresh and can be shared without copying prompt or response data.
It also exposes non-secret credential tags, streaming time-to-first-token, retry and fallback counts, cache
kind, organization scope, and whether token counts came from the provider or
the bounded estimator. Estimated counts are never presented as exact provider
metering.
Prompts, responses, bearer credentials, upstream URLs, and raw provider error
strings are excluded. Content storage is disabled and the effective 730-day
ClickHouse retention plus the 90-day maximum query window are shown in the UI.
Tag Management registers existing virtual-key tags as durable model policies.
For a key carrying multiple managed tags, every enabled tag must allow the
requested public model; disabled tags deny access, and `/v1/models` hides models
that the tag intersection would reject. Unregistered legacy tags remain
metadata-only so introducing the registry does not invalidate existing keys.
Routing diagnostics expose circuit, adaptive EWMA, admission, guardrail, and
shadow-routing state using endpoint names only; provider base URLs and secrets
are not part of the response contract. The Playground discovers only models
authorized for the current key and supports Chat Completions and Responses,
incremental SSE with a single-response JSON fallback for deployments without
native streaming, cancellation, optional provider-safe generation settings,
and in-memory conversation continuity. It sends a stable `X-Session-ID` for each
conversation; Chat resubmits its bounded text transcript while Responses uses
`previous_response_id`. New session clears content and continuation state, and
the console never persists prompts, responses, or raw stream events.
Virtual-key tokens are shown once after creation or rotation and are cleared
from the page when that dialog closes; list responses contain metadata only.
Virtual keys can also select one or more enabled Access Groups through the same
searchable chip control used for models. Access-group model and tool grants are
unioned across the selected groups, then intersected with the key's direct
grants. An assigned group with no grant for a dimension grants nothing in that
dimension; explicit `*` is required for unrestricted access. Missing or disabled
assigned groups fail closed, `/v1/models` applies the effective model policy,
and fallback targets are checked against it before routing.
The Access Groups console provides route-based list and detail views with
configured project/model/tool selectors, attached-key pagination, safe budget
projections and a non-revoked reference count. Normal deletion is rejected while
any non-revoked key still references the group, preventing an accidental policy
outage; operators must remove assignments or revoke those keys first.
The Projects console resolves owner teams from the directory and provides a
route-based workspace over the project's access groups, effective model/tool
grants, and deduplicated non-revoked virtual-key impact. Repeating
`access_group_id` on the key-list API applies server-side any-of filtering and
returns an exact total without downloading the global key inventory; UI rows
remain bounded to 500. A project is deliberately an access-policy container,
not a separate runtime or billing identity, so the console does not invent
project spend that request attribution cannot prove.
The virtual-key table joins the 30-day usage projection by non-secret key ID,
showing spend per currency and optional request/token columns without combining currencies.
For the returned server page, `expand=financials` performs one scoped billing
request and returns every enabled global/organization/team/user/key budget that
applies to each key, including committed-or-reserved usage, remaining cost or
tokens, and reset time. These policies are shown together because enforcement
applies all of them; route-, provider-, model-, and request-tag budgets remain
request-time constraints and are not misrepresented as key-only limits.
Catalog entries can be created, edited, and removed. The dedicated Budgets
console creates, edits, filters, and soft-disables policies while showing live
cost/token utilization, risk state, remaining reset window, and currency for
each policy without combining currencies. Its form resolves organizations,
users, teams, virtual keys, models, providers, and tags from their configured
registries instead of requiring operators to copy opaque IDs. The list uses one
bounded `expand=summaries` request rather than an N+1 summary fan-out. Every mutation uses
the same authenticated admin API and append-only audit path as direct API
clients. The UI has no CDN or runtime package dependency and is protected by a
strict same-origin CSP. Before a credential is stored, `GET /admin/v1/session`
validates it and returns only bounded identity, scope, and console-capability
metadata. Navigation is filtered by those capabilities: global management pages
require `admin`, team-directory pages also support the scoped `team_admin` role,
and inference/API-reference pages remain available to otherwise valid gateway
credentials. Backend RBAC remains authoritative for every request. The bearer
credential is stored only in the current tab's `sessionStorage` and is sent
exclusively to same-origin gateway APIs.

The UI is enabled by default. Disable it with `ADMIN_UI_ENABLED=false` or Helm
`gateway.adminUI.enabled=false`. Static UI pages are intentionally accessible
without authentication, while every data request still passes through the
normal authentication and admin-RBAC pipeline. Operators should expose `/ui/`
only over HTTPS outside local development.
It uses OpenAPI 3.1 and covers inference JSON/SSE responses, multimodal content,
function and MCP tools, virtual-key administration, authentication, error
responses, rate-limit headers, and operational endpoints. Contract tests validate
the document, its examples and references, and require every registered gateway
route to have exactly one matching OpenAPI operation. Internal service endpoints
such as `/authorize`, `/usage`, `/scan`, and `/anonymize` are intentionally not
published.

The gateway can serve the same contract through a self-contained Swagger UI:

```bash
API_DOCS_ENABLED=true go run ./cmd/gateway
# UI:   http://localhost:8080/docs/
# YAML: http://localhost:8080/openapi.yaml
```

Swagger UI assets are embedded in the gateway binary and never loaded from a
CDN. Documentation is disabled by default, including in Helm. When enabled it
is read-only by default: credentials are not persisted, URL query parameters
cannot override the UI configuration, and Swagger's `Try it out` is disabled.
Set `API_DOCS_TRY_IT_OUT_ENABLED=true` (Helm:
`gateway.apiDocs.tryItOutEnabled`) only in a trusted environment where
browser-originated calls to the gateway are intended.

CI validates the contract independently with `oasdiff`. Pull requests are also
compared with the exact base commit and are rejected on definite or potential
breaking changes (`ERR` and `WARN`). The comparison remains inside the GitHub
runner; external references and hosted review uploads are disabled. An
intentional breaking change therefore requires a separately reviewed adjustment
to the compatibility policy rather than silently weakening the gate.

### Multiple providers and failover

Provider endpoints are configured through the `gateway.providers` list in the Helm values. A provider type can have multiple connections with different keys and priorities:

```yaml
gateway:
  provider:
    default: openrouter
  providers:
    - name: openrouter-key-a
      type: openai-compatible
      base_url: https://openrouter.ai/api
      api_key: key-a
      dlp_enabled: true
      av_enabled: true
      models:
        - openai/gpt-4o-mini
      priority: 10
      enabled: true
    - name: openrouter-key-b
      type: openai-compatible
      base_url: https://openrouter.ai/api
      api_key: key-b
      models:
        - openai/gpt-4o-mini
      priority: 20
      enabled: true
    - name: ollama-local
      type: ollama
      base_url: http://host.docker.internal:11434
      max_parallel_requests: 2
      queue_capacity: 8
      queue_timeout_ms: 5000
      priority: 100
      enabled: true
```

### Versioned model catalog

Gateway routing and billing accept the same `MODEL_CATALOG_JSON` schema. Each
entry can be scoped to a deployment ID, managed provider ID, provider type, or `*`, and records model
capabilities, token limits, prices per one million tokens, and currency. Matching
uses deployment ID first, then managed provider ID, provider type, and `*`; a
deployment-specific price therefore overrides a provider-wide price. The Admin
UI uses `(provider_id, public model)` as its stable identity and keeps deployment
IDs as availability metadata rather than creating duplicate model rows.
With `PROVIDER_CONTROL_PLANE_POSTGRES_DSN`, runtime catalog updates are part of
the same versioned PostgreSQL snapshot as deployments and model groups. Redis
is retained for caches and revision hints, but is not the catalog source of
truth. Without a control-plane store, Redis remains the backward-compatible
runtime catalog store and the bundled chart enables AOF-backed persistence.

The gateway derives required capabilities from each request (`chat`,
`responses`, `embeddings`, `stream`, `tools`, and `structured_output`) and excludes catalog
entries that cannot satisfy them or whose `max_output_tokens` is too small.
Billing records `catalog_version`, `pricing_key`, and both rates in every usage
event. The reserve transaction persists that snapshot, so a catalog rollout or
model removal between reserve and commit cannot reprice an in-flight request.

Set `unknown_model_policy` to `deny` in production to fail closed. The default
`legacy` mode preserves compatibility by using endpoint capabilities and the
legacy `BILLING_*_PRICE_PER_1K` values for unknown models. Keep
`gateway.modelCatalog` and `billing.modelCatalog` identical when installing the
charts as separate releases. See
[`docs/model-catalog.example.json`](docs/model-catalog.example.json); its model
names and prices are illustrative configuration values, not a provider price
quote.

Administrators can replace the active catalog without restarting gateway pods
through `GET` and `PUT /admin/v1/model-catalog`. The PUT body uses the same
versioned schema as `MODEL_CATALOG_JSON`. With PostgreSQL configured, the validated
document is committed under optimistic revision control and shared by all
replicas; without PostgreSQL the registry falls back to Redis or process-local
development state. Priced runtime entries must
declare a three-letter currency. The router sends billing the exact selected
version, pricing key, rates, and currency, and reserve pins that snapshot for
commit. If the durable write is unavailable, an update fails and the last valid
catalog remains active.

The Model Onboarding UI uses `POST /admin/v1/model-onboarding/plan` as a
non-mutating server-side validation pass. It then sends the identical catalog,
deployment and model-group set plus the returned `expected_revision` to
`POST /admin/v1/model-onboarding/apply`. Apply revalidates all references and
commits the three resources in one control-plane write. Concurrent edits return
`409 revision_conflict`; persistence errors restore the complete previous
runtime snapshot. Providers and encrypted credentials remain independently
managed prerequisites so their lifecycle can be shared by many deployments.

The Deployments view reads latest health state through one batch endpoint and
runs selected checks through a server-side batch limited to 50 deployments and
four concurrent upstream probes. This avoids browser-side N+1 requests and
unbounded fan-out while retaining the existing 50-entry sanitized history per
deployment. Probe payloads, credentials, and raw provider errors are not stored.

Runtime pricing fields are accepted by billing only on lifecycle calls carrying
the scoped `BILLING_SHARED_SECRET`. This secret is independent from the client
Bearer token, `MANAGEMENT_SHARED_SECRET`, and
`BILLING_MANAGEMENT_SHARED_SECRET`.

Every configured admin mutation writes an append-only PostgreSQL audit pair:
an `attempted` event is committed before the side effect, followed by a
`succeeded` or `failed` outcome event. This covers virtual keys, budget policies,
and runtime model-catalog replacement. If the initial audit append is
unavailable, the mutation fails closed. Administrators can page and filter the
journal with `GET /admin/v1/audit/events?before_id=&limit=&actor_id=&action=`;
request IDs, actor IDs, credential fingerprints, targets, and outcomes are
stored, while Bearer tokens and request bodies are never included.

The gateway tries endpoints in `priority` order. If an endpoint returns an error or is unavailable, the router automatically tries the next compatible endpoint.

Retries and cooldown are configured per endpoint with `max_retries`,
`cooldown_after_failures`, and `cooldown_seconds`. Only transient failures are
retried on the same endpoint; invalid requests and content-policy rejections are
terminal, and post-response failures never trigger a second model generation.
Before a retry, the router waits with exponential backoff starting at 200 ms,
capped at 2 seconds, plus up to 100 ms of jitter. A provider `Retry-After-Ms` or
`Retry-After` value (seconds or HTTP date) takes precedence when it is positive
and no greater than 60 seconds. The next attempt is not started when the wait
would consume the parent request deadline. Streaming calls use the same
scheduler only before the first response chunk or event has been written.
When Redis is configured, failure counters, `open_until`, and a single half-open
probe lease are shared by every gateway replica. Without Redis the same circuit
states remain process-local. A transient Redis error falls back to the local
tracker while readiness continues to report the Redis dependency failure.

Provider concurrency is bounded per endpoint with `max_parallel_requests`.
`queue_capacity` and `queue_timeout_ms` optionally add a bounded waiting queue;
both require a positive concurrency limit, and a positive queue capacity also
requires a positive timeout. A saturated endpoint is skipped in favor of the
next compatible endpoint. If every candidate is saturated, the gateway returns
`429 provider_busy` with `Retry-After`. A slot covers the complete provider call,
including same-endpoint retries or the lifetime of an SSE stream.

### Shadow traffic mirroring

An endpoint with `shadow: true` is never selected as a primary/fallback route
and is omitted from `/v1/models`. `mirror_percentage` (default `100`) selects a
deterministic subset by request ID; `mirror_timeout_ms` (default `5000`) bounds
each detached asynchronous call. Mirroring starts only when a primary provider
call is actually needed, so exact/semantic cache hits are not duplicated.

The Caching console turns `/admin/v1/cache/diagnostics` into separate exact and
semantic current-replica health cards with hit ratio, hits, misses, writes,
errors, TTL/capacity metadata, and operation/result counters. It deliberately
does not combine proxy cache hits with provider-reported prompt-cache tokens;
the latter remain in Usage and request Logs. Runtime cache settings stay
deployment-managed and are read-only in the console.

The shadow receives a deep-cloned provider DTO after required DLP, AV, and
anonymization modules have completed. It receives neither client Bearer/API
keys nor the deanonymization map. Shadow responses are discarded, are not
billed, never affect primary circuit/fallback state, and cannot delay or alter
the client response. Configure independent shadow admission limits because the
shadow provider may still incur external cost; `max_parallel_requests` is
therefore required for every shadow endpoint.

The auth service supports virtual keys through PostgreSQL management APIs and,
for migration fallback, `AUTH_VIRTUAL_KEYS_JSON` (Helm: `auth.virtualKeys`).
Managed keys include an alias, description and tags plus `team_id`, `roles`,
`access_group_ids`, `allowed_models`, `allowed_tools`, `rate_limit_rpm`,
`rate_limit_tpm` and expiry.
Admins can update policy without changing the bearer secret, temporarily
disable/re-enable a key, rotate it atomically, revoke it, open its metadata-only
request history, and assign a key-scoped budget from the console. The gateway
key-list API exposes bounded server-side pagination, ownership/status filters,
and allowlisted sorting; team and organization scopes include member-owned keys.
The console sends those parameters to the server with a debounced alias search
and uses the returned total for page navigation. Budget columns are intentionally
not sorted: a key can have several simultaneously enforced policies in different
currencies and dimensions, so one scalar ordering would be misleading.
Each row links to `/ui/api-keys/{id}`, a metadata-only workspace that joins
configured ownership names, direct grants, access groups, currency-separated
30-day usage, daily activity and every applicable budget policy. Rotation,
disable/enable and revoke retain the existing audited lifecycle; a rotated
plaintext token is still displayed exactly once. Settings edits return to the
same configured-selector form used by the catalog rather than introducing raw
owner IDs.
The gateway
receives only the opaque key ID, alias, tags and policy (never the plaintext
token or lookup hash), filters `/v1/models`, enforces model grants before the
provider call, and applies the rate-limit policy through a replaceable atomic
store interface. If `REDIS_ADDR` is configured, RPM/TPM admission is performed
atomically in Redis; otherwise the gateway uses the process-local implementation.

The identity directory exposes admin APIs and console views for users, teams,
global roles and team memberships. Global `admin` credentials can manage the
whole directory; `team_admin` credentials are restricted to listing and
managing memberships in their own `team_id` and cannot edit global user roles.
Teams link to a dedicated details workspace that joins membership-scoped roles,
configured user identities and safe virtual-key metadata. Adding a member uses
the visible configured-user registry rather than a manually entered ID; role
updates and removals are audited and enforced by the same team scope in the API.
Disabling a directory user or team immediately prevents its persistent virtual
keys from authorizing while preserving audit and usage history.

Model deployments expose a safe runtime control surface over endpoints already
configured by operators. Admins can change model bindings, capabilities,
priority, weight, guardrail policy and enabled state atomically; new routing
decisions observe the update immediately. The API returns provider type and
bounded health signals but never base URLs, API keys or secret references.
When the control-plane DSN is configured, these overrides are part of the
versioned durable snapshot and are refreshed across replicas. Without that
store they remain process-local runtime state.

Guardrail policies are also hot runtime state: each named policy enables DLP,
AV, or both. A policy can be selected by a model deployment or activated by a
durable policy attachment. Attachments support global scope or an intersection
of team ID, virtual-key ID/alias, public model and key-tag selectors; exact and
trailing-`*` prefix patterns are supported. Matching attachments are combined,
so every requested check runs even when the chosen deployment has no guardrail
configured. A missing, disabled or unavailable attached policy fails closed.
The Compliance
Playground sends a bounded text projection directly to those internal scanners,
never invokes a model, never echoes the submitted text, and reports
`content_stored: false`. A rejected scanner produces an explicit deny decision;
an unavailable enabled scanner fails closed with HTTP 503.

The MCP registry stores only approved HTTPS endpoint metadata, transport type,
and canonical tool identifiers. It deliberately has no credential, header, or
secret fields, and rejects URLs containing user info, query parameters, or
fragments. Toolsets group exact or prefix-wildcard tool identifiers; virtual
keys receive them through `allowed_tools` grants such as `toolset:weather`.
Disabled toolsets stop authorizing immediately. Registry changes are audited
and use the same durable admin-state snapshot when configured. The management
API can expand server and toolset references. Server deletion is rejected while
a toolset consumes one of its grants; toolset deletion enumerates the complete
non-revoked virtual-key directory plus Access Groups and fails closed while an assignment
exists or the directory cannot prove a complete result.

Customer Insights joins scoped metadata-only usage with the already enforced
budget and virtual-key policies for a user, team, or key ID. ClickHouse scope
values use typed query parameters rather than SQL interpolation. The console
shows currency-separated spend, tokens, error rate, applicable global/scoped
budgets, and RPM/TPM key limits without exposing prompts or bearer secrets.

Organizations persist a global-admin-managed tenant hierarchy that assigns each
team to at most one organization. The Organizations table links to a route-based
details workspace that resolves configured teams and organization-scoped virtual
keys. Assignments use configured-team selection; a team already owned elsewhere
is rejected with HTTP 409 rather than silently reparented, and must first be
removed from its current organization. The AI Hub joins public catalog metadata with
safe deployment availability and never returns endpoint URLs or credentials.
Cost Optimization flags unavailable and unpriced entries and compares catalog
input-plus-output prices only for the same model and currency. Reported savings
are deterministic catalog comparisons (`usage_projection: false`), not forecasts
of customer spend.

`GET /metrics` exposes Prometheus-format HTTP, provider-attempt, cache,
module, security, and billing lifecycle counters plus duration sums. Labels are
bounded: unmatched URLs become `path="unmatched"`, results use a fixed enum, and
identity, prompt, arbitrary model names, and secrets are never metric labels.
Every response carries `X-Request-ID`, and the gateway emits one JSON request log
with the same ID, trace/span IDs, status, normalized path, and duration.

Set `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` to a full OTLP/HTTP traces URL such as
`http://otel-collector:4318/v1/traces`. `OTEL_TRACE_SAMPLE_RATIO` accepts a value
from `0` to `1`; an empty endpoint disables exporting but the in-process SDK
still creates correlation trace IDs. Incoming W3C trace context is continued,
remote modules and provider HTTP calls propagate it, and gateway, module,
provider-attempt, and HTTP client spans are flushed during graceful shutdown.
Final billing outcomes persist the same metadata-only `trace_id` in ClickHouse;
prompt, response, credential, and raw provider error content remain excluded.

Named guardrail profiles are configured
with `GUARDRAIL_POLICIES_JSON` (Helm: `gateway.guardrailPolicies`) and selected
per endpoint with `guardrail_policy`; an endpoint referencing an unknown profile
is skipped instead of running without the intended DLP/AV controls. Runtime
attachments are managed through `/admin/v1/policy-attachments` or the Policies
page and are stored in the versioned control-plane snapshot.

Guardrail Monitor stores only bounded evaluation metadata: request ID, policy,
module, source, outcome, duration and timestamp. Prompts, responses and raw
scanner details are never retained. With `REDIS_ADDR`, the history is shared by
gateway replicas; if Redis is unavailable the report explicitly falls back to
the bounded current-replica buffer. Configure its maximum event count with
`GUARDRAIL_MONITOR_CAPACITY` (Helm: `gateway.guardrailMonitor.capacity`, default
1000) and Redis expiry with `GUARDRAIL_MONITOR_TTL_SECONDS` (Helm:
`gateway.guardrailMonitor.ttlSeconds`, default seven days). Invalid values fail
gateway startup instead of silently changing retention.

Model groups can be managed at runtime as a public model name plus an ordered
set of deployment IDs. `priority` defines fallback tiers; endpoints at the same
priority use `weight` for weighted round-robin or adaptive EWMA ordering. Static
configuration can still use endpoint-specific `model_aliases`, for example
`{"fast":"deployment-gpt-5-mini"}`. `capabilities` can restrict an endpoint to `chat`,
`responses`, `embeddings`, `rerank`, `vision`, `mcp`, and/or `stream`.

The Model Groups page exposes the same routing model without requiring operators
to remember deployment IDs: deployments are selected from a searchable multi-select,
membership order can be changed explicitly, and each member shows its provider,
upstream model, fallback priority, weight, runtime state and latest health result.
Health reads are batched by 200 deployments and active checks by 50, matching the
admin API limits. Group retry overrides are limited to transient failure classes;
empty values continue to use each deployment's default.

`GET /admin/v1/model-groups/{id}/routing-settings` returns the complete group,
its ordered deployments, and the current control-plane revision. The Router
Settings page preserves that revision and submits the complete plan to the
corresponding `PUT` endpoint. Group membership, strategy, retry overrides,
deployment priority and operational retry/cooldown settings are validated first
and then persisted in one optimistic transaction. A stale revision, invalid
member, or persistence failure leaves every affected object unchanged. Model
Groups links directly to this workspace with `?group=<public-model>`.

Each model group may also define ordered cross-model fallback chains for
`general`, `context_window`, and `content_policy`. General fallback is limited
to normalized timeout, rate-limit, unavailable, and unknown provider failures;
client validation, client/provider authentication, post-processing, budget,
and gateway guardrail failures remain terminal. Context-window and upstream
content-policy fallbacks run only when explicitly configured for that class.
The graph rejects missing targets, self-references, duplicates, and cycles, and
a referenced target cannot be deleted. A chain is stored in the same atomic,
revision-guarded routing transaction as group membership and deployment
settings.

Before inference, the HTTP policy layer intersects every possible fallback
target with the virtual key's model grants and all managed tag constraints.
Unauthorized targets are omitted from the route. Guardrail attachments for the
primary and every authorized target are combined conservatively, so changing
models cannot bypass a stricter policy. The original public model remains the
billing/logging identity; the concrete endpoint and upstream model identify the
route that actually served it. Routing simulation accepts a normalized
`failure_class` and returns primary and fallback stages with the trigger that
selected them. Streaming can switch groups only before the first SSE payload is
written.

Chat Completions and Responses accept OpenAI-style image parts for endpoints
that explicitly declare `vision`, implement a vision-capable adapter, and enable
AV. OpenAI-compatible endpoints preserve the original blocks, Anthropic receives
native base64 image sources, and native Ollama chat receives its `images` array.
Only inline `data:image/{jpeg,png,gif,webp};base64,...` inputs are accepted;
remote URLs are rejected to avoid SSRF and unscanned content. Limits are 8
images, 8 MiB each, 16 MiB decoded in total, and 24 MiB for the JSON request.
Image signatures must match the declared media type.

DLP receives only the text projection. AV receives that projection plus
separate binary attachments and scans each attachment through ICAP with its
image media type. Image payloads and URLs are removed from the remote
anonymizer request and restored unchanged after its text-only response. An AV
failure on a multimodal request is fail-closed even when AV is otherwise
configured as optional.

Virtual keys can restrict tools with `allowed_tools`. Empty grants preserve
backward-compatible access to all request tools; otherwise exact names and
trailing-wildcard prefixes are enforced before rate limiting or provider
routing. Chat function tools use their function name (namespaced names such as
`mcp.weather.forecast` are supported). Responses MCP connectors use the full
identity `mcp:<server_label>@<canonical-https-url>`, for example
`mcp:weather-prod@https://mcp.example.test`. Non-HTTPS URLs and URLs containing
userinfo, query parameters, or fragments are rejected. Invalid tool definitions
return 400 and disallowed tools return 403 without reaching DLP, billing, or a
provider.

Responses MCP passthrough requires both an adapter implementing the MCP contract
and an endpoint/catalog entry explicitly declaring `mcp`; legacy empty
capabilities are not an opt-in. Connector URL, connector-level allowed tools,
approval policy, and scoped headers are forwarded only to the selected
OpenAI-compatible provider.

Set `ROUTING_STRATEGY=adaptive` (Helm: `gateway.routing.strategy`) to rank
same-priority endpoints by an EWMA of observed latency and failures. Unknown
endpoints are explored before the router converges, priority remains a hard
boundary, and cooldown/failover behavior is unchanged. Tune smoothing with
`ADAPTIVE_ROUTING_EWMA_ALPHA`; the default weighted strategy preserves static
weighted round-robin.

Responses sessions are tenant-scoped and pinned to the endpoint that created
their response ID for `RESPONSES_AFFINITY_TTL_SECONDS` (default one hour). Redis
makes the mapping shared across replicas; without Redis it is process-local.
Affinity keys hash the tenant and response ID. A continuation never sends a
provider-scoped `previous_response_id` to a different endpoint; if the pinned
endpoint is unavailable the request fails explicitly instead of silently
breaking session state.

Exact caching is disabled by default and enabled with
`EXACT_CACHE_TTL_SECONDS` (Helm: `gateway.exactCache.ttlSeconds`). It runs inside
the provider pipeline: DLP/AV still execute, cached payloads are stored after
anonymization, cache keys include the team or credential, and billing receives a
cache-hit event with zero provider tokens. With `REDIS_ADDR`, cache entries are
shared by gateway replicas; without it the cache is process-local.

Semantic caching is a separate opt-in feature (`SEMANTIC_CACHE_TTL_SECONDS`;
Helm: `gateway.semanticCache`). It applies only to non-streaming, text-only chat
requests containing exactly one user message plus optional exact-matched
system/developer messages, without tools, tool results, assistant history, or
structured-output contracts. The cache
is always scoped to the irreversible credential ID, authenticated user, endpoint,
logical model, and generation settings; it is never shared merely because two users belong to
the same team. Entries are process-local, TTL-bound, entry-count-bound, and
payload-size-bound.

The semantic embedder runs after DLP/AV and anonymization, receives no client
bearer, and uses only `SEMANTIC_CACHE_EMBEDDING_API_KEY`. Configure its
OpenAI-compatible URL/model and tune `SEMANTIC_CACHE_THRESHOLD` conservatively
(default `0.95`). Embedder failures fail open to the normal provider path.
Semantic hits keep the ordinary billing lifecycle but commit zero provider
tokens. Embedding calls may still incur cost at the configured embedding
provider and should be monitored separately. Because approximate matches can be
wrong, keep the feature disabled for high-risk or rapidly changing answers.

Request with an explicit provider:

```json
{
  "provider": "openrouter",
  "model": "openai/gpt-4o-mini",
  "messages": [
    {"role": "user", "content": "hello"}
  ]
}
```

The `provider` field accepts either a provider `type` (`ollama`, `openai-compatible`, or `openrouter`) or a specific endpoint name such as `openrouter-key-a`.

### DLP and antivirus through ICAP

The `dlp` and `av` modules run at the provider level before anonymization and before the request is sent to an AI provider endpoint. The gateway invokes them as remote HTTP modules, while the separate `repos/dlp` and `repos/av` microservices perform the ICAP calls. DLP receives text only; AV additionally receives validated image attachments for multimodal requests and scans their decoded bytes separately.

Enable these modules separately for each endpoint in `PROVIDERS_JSON` or Helm `gateway.providers`:

```yaml
gateway:
  modules:
    dlp:
      required: true
      remote: true
    av:
      required: true
      remote: true
  providers:
    - name: openrouter-key-a
      type: openai-compatible
      dlp_enabled: true
      av_enabled: true
dlp:
  enabled: true
  icap:
    host: dlp.example.local
    port: "1344"
    service: /reqmod
av:
  enabled: true
  icap:
    host: av.example.local
    port: "1344"
    service: /avscan
```

When running without Helm, use these variables:

```text
DLP_URL=http://localhost:8084
AV_URL=http://localhost:8085
DLP_REQUIRED=true
AV_REQUIRED=true
```

Configure the ICAP connections for the `dlp` and `av` microservices with these environment variables:

```text
DLP_ICAP_HOST=dlp.example.local
DLP_ICAP_PORT=1344
DLP_ICAP_SERVICE=/reqmod
DLP_ICAP_TIMEOUT=5s
AV_ICAP_HOST=av.example.local
AV_ICAP_PORT=1344
AV_ICAP_SERVICE=/avscan
AV_ICAP_TIMEOUT=5s
```

If an endpoint does not define `dlp_enabled` or `av_enabled`, the gateway does not call the corresponding microservice. The microservices send the request text using ICAP `REQMOD`. An ICAP `204` response or a regular successful `2xx` response allows the request to proceed. Block or infection indicators in the ICAP headers are returned to the gateway as a `451` response and stop the current provider attempt.

### Authentication: API keys and JWTs

The gateway reads the token from the standard header:

```text
Authorization: Bearer <token>
```

The authentication service first checks the PostgreSQL virtual-key store when
`AUTH_POSTGRES_KEYS_ENABLED=true`. Active keys can carry team, role, model, RPM,
TPM, and tool policy; expired or revoked keys do not authorize. The database contains
only an HMAC-SHA256 token lookup value and an opaque key ID. Linked key rotation
creates a replacement and revokes the previous key in one transaction.

The auth service also accepts OIDC access tokens through a direct JWKS endpoint.
Set `auth.jwt.jwksUrl` (or `AUTH_JWT_JWKS_URL`) together with an exact issuer and
audience. JWKS mode accepts RS256/ES256, requires token expiry, caches signing
keys, and refreshes on an unknown `kid`; HS256 remains available only when no
JWKS URL is configured. `userIdClaim`, `teamIdClaim`, and `rolesClaim` support
dot-separated nested claim paths. JWKS availability participates in `/readyz`.

An authenticated caller with the `admin` role can manage persistent virtual
keys through `POST /admin/v1/keys`, `POST /admin/v1/keys/{id}/rotate`, and
`DELETE /admin/v1/keys/{id}`. Create and rotate accept the complete key policy
(`user_id`, optional team/roles/model grants/rate limits/expiry) and return the
new plaintext token exactly once. There is deliberately no list endpoint and no
API for reading token hashes.

The gateway validates the client bearer through auth, clears it, and calls auth
management with a separate `MANAGEMENT_SHARED_SECRET`, request ID, and
irreversible actor credential ID. The two charts must receive the same secret
from an encrypted values source; never reuse `AUTH_KEY_HASH_SECRET`, a provider
key, or a client bearer for it. Direct auth management requests without the
scoped secret and audit identity are rejected.

Example create policy:

```json
{
  "user_id": "user-42",
  "team_id": "team-a",
  "roles": ["developer"],
  "allowed_models": ["gpt-*"],
  "allowed_tools": ["mcp.weather.*", "mcp:weather-prod@https://mcp.example.test"],
  "rate_limit_rpm": 60,
  "rate_limit_tpm": 100000,
  "expires_at": "2027-01-01T00:00:00Z"
}
```

For migration and local development, static and demo fallbacks are controlled
independently. The built-in demo API keys are:

- `demo-admin-key`
- `demo-user-key`

If the key is not found, the service tries to validate it as a JWT. This example
shows legacy HS256; set `AUTH_JWT_JWKS_URL` for the recommended OIDC mode:

```text
AUTH_JWT_SECRET=dev-jwt-secret
AUTH_JWT_ISSUER=ai-gateway
AUTH_JWT_AUDIENCE=ai-gateway
AUTH_JWT_JWKS_URL=
AUTH_POSTGRES_KEYS_ENABLED=true
AUTH_POSTGRES_DSN=postgres://ai_gateway:password@postgres:5432/ai_gateway
AUTH_KEY_HASH_SECRET=separate-random-pepper
AUTH_STATIC_KEY_FALLBACK_ENABLED=false
AUTH_DEMO_KEYS_ENABLED=false
```

Minimum claims:

```json
{
  "sub": "user-123",
  "roles": ["developer"],
  "iss": "ai-gateway",
  "aud": "ai-gateway",
  "exp": 1790000000
}
```

By default, `sub` becomes `UserID`, `team_id` becomes `TeamID`, and `roles` or
`role` is passed into the role model. The three claim paths are configurable and
may address nested objects. In legacy HS256 mode, store the secret as a
Kubernetes Secret rather than in a regular `values.yaml` file.

### Anonymization configuration

Use the `ANONYMIZER_RULES` variable or the `anonymizer.rules` Helm value to choose the enabled rules. Rule definitions live in `anonymizer.ruleDefinitions` in the Helm chart. Each rule can define a `name`, `placeholder`, regular-expression `pattern`, and optional `validator`. The `luhn` validator is currently used for bank card numbers. When the definitions change, Helm updates the ConfigMap and automatically restarts the anonymizer deployment.

Anonymization is reversible within a single provider attempt. Before sending a request to a specific AI provider endpoint, the router replaces sensitive values with unique placeholders such as `{{EMAIL_1}}`. After the model responds, it restores the original values in the text returned to the user.

Available rules:

- `email` — email addresses.
- `phone` — phone numbers.
- `person_ru` — full names in Russian.
- `address_ru` — addresses containing Russian abbreviations such as `г.`, `ул.`, `д.`, and `кв.`.
- `passport_ru` — Russian passport numbers in the `4510 123456` format.
- `inn` — Russian taxpayer identification numbers containing 10 or 12 digits.
- `bank_card` — bank card numbers validated with the Luhn algorithm.
- `ip` — IPv4 addresses.
- `api_key` — API keys, access tokens, bearer tokens, and client secrets.
- `jwt` — JWTs.
- `secret` — passwords and similar secrets.

Examples:

```powershell
# Enable all rules
helm upgrade --install ai-gateway-anonymizer charts/anonymizer --namespace ai-gateway --set anonymizer.rules=all

# Enable only email, phone, and IP rules
helm upgrade --install ai-gateway-anonymizer charts/anonymizer --namespace ai-gateway --set-string anonymizer.rules="email\,phone\,ip"

# Disable all rules
helm upgrade --install ai-gateway-anonymizer charts/anonymizer --namespace ai-gateway --set anonymizer.rules=none
```

Test the gateway:

```powershell
kubectl -n ai-gateway port-forward svc/ai-gateway-gateway 18080:8080
```

In another terminal:

```powershell
Invoke-RestMethod -Method Post http://127.0.0.1:18080/v1/chat/completions `
  -Headers @{ Authorization = "Bearer demo-admin-key"; "Content-Type" = "application/json" } `
  -Body '{"model":"demo-model","messages":[{"role":"user","content":"hello from user@example.com"}]}'
```

For a production-style deployment using custom images:

```powershell
docker build -t ai-gateway:dev .
helm upgrade --install ai-gateway charts/ai-gateway --namespace ai-gateway --set devMode.enabled=false
helm upgrade --install ai-gateway-auth charts/auth --namespace ai-gateway --set devMode.enabled=false
helm upgrade --install ai-gateway-anonymizer charts/anonymizer --namespace ai-gateway --set devMode.enabled=false
helm upgrade --install ai-gateway-billing charts/billing --namespace ai-gateway --set devMode.enabled=false
helm upgrade --install ai-gateway-security charts/security --namespace ai-gateway --set devMode.enabled=false
```

## Structure

```text
cmd/
  gateway/
  anonymizer/
  auth/
  billing/
internal/
  config/
  gateway/
  modules/
  openai/
```

## Split repositories

For easier ongoing development, the monorepo is split into independent Go projects:

```text
repos/
  gateway/
  auth/
  billing/
  dlp/
  av/
  anonymizer/
```

Each directory contains its own `go.mod`, `Dockerfile`, and `README.md`. To test a project:

```powershell
cd repos/gateway
go test ./...
```

OpenAI-compatible DTOs and the service-specific HTTP contracts are currently duplicated across the services. The next step is to move them into a separate versioned module or repository, such as `ai-gateway-contracts`, so the DTOs do not need to be synchronized manually. The internal gateway `RequestContext` is deliberately not a network contract: bearer credentials are sent only to auth, and every other service receives a minimal typed request.

## GitHub Actions and releases

Pull requests and pushes to `main` or `master` run Go formatting, vet, race-enabled tests, container builds, and Helm lint/render checks. A semantic version tag starts a release:

```powershell
git tag v0.2.0
git push origin v0.2.0
```

The release workflow publishes multi-platform service images to GitHub Container Registry using names such as `ghcr.io/<owner>/ai-gateway-gateway:0.2.0`. It also publishes every chart to the OCI namespace `oci://ghcr.io/<owner>/charts` and attaches the packaged charts to the GitHub Release.

Install a published chart with its matching service image:

```powershell
helm upgrade --install ai-gateway oci://ghcr.io/<owner>/charts/ai-gateway `
  --version 0.2.0 `
  --namespace ai-gateway `
  --create-namespace
```

The workflows use the built-in `GITHUB_TOKEN`; no additional registry secret is required. In the repository settings, keep Actions workflow permissions enabled and allow the workflow to write packages and repository contents.

New GHCR packages are private by default. For a public repository, change the visibility of the published service images and Helm chart packages to public after the first release.

## Roadmap

The gateway already has typed remote module contracts, OpenAI-compatible and
Anthropic adapters, chat/Responses streaming, Redis-backed distributed limits
and exact cache, a PostgreSQL billing ledger with a durable ClickHouse outbox,
tools/structured output, PostgreSQL virtual keys, atomic multi-scope budgets,
a versioned capability/pricing catalog, OpenTelemetry observability, an
embeddings API with security and billing parity, and adaptive routing with
tenant-scoped Responses affinity, plus an admin-RBAC virtual-key management API.
It also has an opt-in, credential-scoped semantic cache for constrained text-only
chat requests, credential-level function/MCP tool ACLs, and bounded multimodal
image input with explicit vision routing and fail-closed binary AV scanning.
Admin RBAC also exposes CRUD for global/organization/key/user/team/model/provider/tag budget policies and currency-isolated current-window
summaries through `/admin/v1/budgets?expand=summaries`; gateway-to-billing calls use a separate
`BILLING_MANAGEMENT_SHARED_SECRET`, never the client Bearer token.

## License

This project is licensed under the [GNU Affero General Public License v3.0](LICENSE).
