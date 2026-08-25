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

The gateway serves a self-contained operations console at `/ui/`. It provides
an overview, virtual-key lifecycle management, model and runtime-catalog
management, budget policy management, and a read-only management audit view.
The Usage & Spend view reads final request outcomes from ClickHouse for a
bounded 7/30/90-day window and breaks requests, tokens, latency, and spend down
by day, model, and provider. Costs remain separated by currency.
The Request Logs view provides a bounded, cursor-paginated explorer over the
same final outcomes with filters for request, status, model, endpoint, user,
team, and credential fingerprint. Its detail contract contains operational
metadata, token counts, cost, cache state, and a bounded failure class only.
Prompts, responses, bearer credentials, upstream URLs, and raw provider error
strings are excluded. Content storage is disabled and the effective 730-day
ClickHouse retention plus the 90-day maximum query window are shown in the UI.
Routing diagnostics expose circuit, adaptive EWMA, admission, guardrail, and
shadow-routing state using endpoint names only; provider base URLs and secrets
are not part of the response contract. The playground sends non-streaming chat
requests through the same authenticated inference path as external clients.
Virtual-key tokens are shown once after creation or rotation and are cleared
from the page when that dialog closes; list responses contain metadata only.
Catalog entries can be created, edited, and removed; budgets can be created,
edited, and disabled. Every mutation uses
the same authenticated admin API and append-only audit path as direct API
clients. The UI has no CDN or runtime package dependency and is protected by a
strict same-origin CSP. Sign in with a bearer credential carrying the `admin`
role; the credential is stored only in the current tab's `sessionStorage` and
is sent exclusively to the gateway's existing authenticated APIs.

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
entry can be scoped to an endpoint name, provider type, or `*`, and records model
capabilities, token limits, prices per one million tokens, and currency. Matching
uses endpoint name first, then provider type, then `*`; an endpoint-specific
price therefore overrides a provider-wide price.

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
versioned schema as `MODEL_CATALOG_JSON`. With Redis configured, the validated
document is shared by all replicas and picked up within one second; without
Redis the update is process-local for development. Priced runtime entries must
declare a three-letter currency. The router sends billing the exact selected
version, pricing key, rates, and currency, and reserve pins that snapshot for
commit. If Redis is unavailable, an update fails and the last valid catalog
remains active.

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
`allowed_models`, `allowed_tools`, `rate_limit_rpm`, `rate_limit_tpm` and expiry.
Admins can update policy without changing the bearer secret, temporarily
disable/re-enable a key, rotate it atomically, revoke it, open its metadata-only
request history, and assign a key-scoped budget from the console. The gateway receives only the key
fingerprint and policy, filters `/v1/models`, enforces model grants before the
provider call, and applies the rate-limit policy through a replaceable atomic
store interface. If `REDIS_ADDR` is configured, RPM/TPM admission is performed
atomically in Redis; otherwise the gateway uses the process-local implementation.

The identity directory exposes admin APIs and console views for users, teams,
global roles and team memberships. Global `admin` credentials can manage the
whole directory; `team_admin` credentials are restricted to listing and
managing memberships in their own `team_id` and cannot edit global user roles.
Disabling a directory user or team immediately prevents its persistent virtual
keys from authorizing while preserving audit and usage history.

`GET /metrics` exposes Prometheus-format HTTP, provider-attempt, cache,
module, security, and billing lifecycle counters plus duration sums. Labels are
bounded: unmatched URLs become `path="unmatched"`, results use a fixed enum, and
identity, prompt, arbitrary model names, and secrets are never metric labels.
Every response carries `X-Request-ID`, and the gateway emits one JSON request log
with the same ID, trace/span IDs, status, normalized path, and duration.

Set `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` to a full OTLP/HTTP traces URL such as
`http://otel-collector:4318/v1/traces`. `OTEL_TRACE_SAMPLE_RATIO` accepts a value
from `0` to `1`; an empty endpoint disables exporting. Incoming W3C trace context
is continued, remote modules and provider HTTP calls propagate it, and gateway,
module, provider-attempt, and HTTP client spans are flushed during graceful
shutdown.

Named guardrail profiles are configured
with `GUARDRAIL_POLICIES_JSON` (Helm: `gateway.guardrailPolicies`) and selected
per endpoint with `guardrail_policy`; an endpoint referencing an unknown profile
is skipped instead of running without the intended DLP/AV controls.

Model groups use endpoint-specific `model_aliases`, for example
`{"fast":"deployment-gpt-5-mini"}`. Endpoints at the same priority can set
`weight`; routing uses weighted round-robin and preserves the remaining members
as failover candidates. `capabilities` can restrict an endpoint to `chat`,
`responses`, `embeddings`, `rerank`, `vision`, `mcp`, and/or `stream`.

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
Admin RBAC also exposes CRUD for multi-scope budget policies and a current-window
spend summary at `/admin/v1/budgets`; gateway-to-billing calls use a separate
`BILLING_MANAGEMENT_SHARED_SECRET`, never the client Bearer token.

## License

This project is licensed under the [GNU Affero General Public License v3.0](LICENSE).
