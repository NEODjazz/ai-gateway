# AI Gateway - Gateway

Central OpenAI-compatible gateway with provider routing, failover, auth integration, and provider-level module execution.

## Run

```powershell
go run ./cmd/gateway
```

## Selected endpoints

The complete route and schema reference is [OpenAPI](api/openapi.yaml).
Chat Completions accepts both modern tool calls and the legacy `functions` / `function_call` contract on compatible deployments. The two contracts are mutually exclusive, use the same function-name authorization policy, participate in token reservation, and bypass semantic caching. Native adapters return an explicit unsupported-parameter error for legacy function calls.

For MCP registry management, client-side tools, Responses passthrough and
permission examples, see [MCP integration](../../docs/mcp.md).

- `GET /healthz`
- `GET /a2a/{agent}/.well-known/agent-card.json`
- `POST /a2a/{agent}`
- `POST /v1/chat/completions`
- `POST /v1/completions`
- `POST /v1/responses`
- `POST /v1/responses/input_tokens`
- `POST /v1/responses/compact`
- `GET /v1/responses/{id}`
- `DELETE /v1/responses/{id}`
- `POST /v1/responses/{id}/cancel`
- `GET /v1/responses/{id}/input_items`
- `GET /admin/v1/session`
- `GET /admin/v1/usage/report`
- `GET /admin/v1/request-logs`
- `GET /admin/v1/request-logs/groups?dimension=session|trace`
- `GET /admin/v1/request-logs/{request_id}`
- `GET /admin/v1/request-logs/settings`
- `GET /admin/v1/projects`
- `GET /admin/v1/projects/{id}`
- `GET /admin/v1/policy-attachments`
- `PUT /admin/v1/policy-attachments/{id}`
- `DELETE /admin/v1/policy-attachments/{id}`

A2A 1.0 direct discovery and synchronous or durable asynchronous `SendMessage` are available for
enabled agent profiles that do not reference an instruction template. The
profile ID is carried as the declared interface tenant. Execution uses the
shared Responses authentication, model authorization, quota, guardrail,
routing and billing path. With durable task and background-response storage,
`returnImmediately=true` returns an owner-scoped submitted or working task;
`GetTask` reconciles completion and `CancelTask` cancels pending execution.
Text and bounded inline JPEG, PNG, GIF and WebP inputs reuse the Responses
media validation, scan, token reserve and billing path. Stream resubscription,
push notifications and other media parts fail with explicit protocol errors.
`GetExtendedAgentCard` returns the current card only after bearer
authentication, model authorization and RPM admission.
With durable task storage, `SendStreamingMessage` emits an ordered SSE
lifecycle: the Task first, bounded artifact deltas, and a terminal status only
after provider post-processing and billing settlement succeed.

Gateway-level module:

- `auth`

Provider-level modules:

- `anonymizer`
- `dlp`
- `av`
- `billing`

The internal `RequestContext` is not serialized between services. Each remote
module has a minimal typed contract; only auth receives the client bearer token,
which gateway clears before provider-level processing.

The Budgets catalog expands current-window summaries in one bounded request.
Budget rows link to a route-based details workspace that reads the authoritative
policy and summary endpoints, preserves currency isolation, links identity
scopes, and returns edits to the configured-target form.

HTTP metric and request-log path labels are derived from the registered route
contracts. Dynamic identities remain placeholders and unknown paths are labeled
`unmatched`, keeping observability complete without unbounded tenant labels.

Guardrail policy definitions remain independent from their request scopes.
Policy attachments can apply an enabled DLP/AV policy globally or to the
intersection of configured team, virtual-key ID/alias, public model, and key-tag
patterns. Matching policies are combined with deployment guardrails and fail
closed if a required scanner is unavailable.
Policies can explicitly require provider-output DLP. The gateway scans the
bounded textual response projection before delivery and routes streaming
requests through its buffered fallback so rejected or unscanned content is not
sent in partial events.
`POST /admin/v1/policy-attachments/resolve` performs a metadata-only dry
resolution with the exact runtime matcher. The Policies workspace uses it to
show matched attachments, cumulative input/output DLP and AV requirements, and fail-closed
missing/disabled-policy issues without invoking a provider or scanner. Its
attachment editor supports configured values plus exact or trailing-asterisk
patterns and displays the AND-across-dimensions scope semantics before save.
Virtual-key details resolve policy impact with the key identity, owner team,
credential tags and selected public model; team details open the same simulator
with their identity context prefilled. The UI therefore shows a runtime result
rather than an inaccurate context-free list of policies on an identity.
Identity workspaces can open the attachment editor with their immutable key or
team ID preselected. The attachment remains an independently persisted runtime
resource, so policy assignment never requires a cross-service key/team update.
The Guardrails UI presents these gateway-native policies as one workflow: joined
deployment/attachment coverage, create/edit, detail inspection, policy-filtered
monitoring and bounded multi-policy dry-run comparison. The gateway keeps scanner endpoints and credentials in
the independently operated DLP/AV services and stores no executable guardrail
code in its control plane.

Request-log session and trace views are aggregated in ClickHouse across the
complete selected time window, rather than from the current browser page.
Aggregation remains metadata-only and keeps spend in separate currency groups;
the cursor consists of the last request time, group ID, and currency. Request
and group views accept bounded minimum/maximum cost and failure-class filters,
and the UI can drill from a session or trace aggregate into its request rows.

Transient same-deployment retries use centralized, deadline-aware exponential
backoff with bounded jitter. Provider `Retry-After-Ms` and `Retry-After` hints
are preserved by every HTTP adapter and honored up to 60 seconds. Streaming is
retried only before the first client-visible chunk or event.

Usage reports expose currency-safe totals and daily trends plus breakdowns by
public and upstream model, provider, endpoint, tag, non-secret virtual-key ID,
user, team, and organization. The report endpoint accepts bounded, typed
filters for each dimension so the UI drill-down is calculated in ClickHouse.
Provider prompt-cache read and write tokens are carried as separate counters in
usage reports and request logs, independently from gateway cache-hit counts.
Exact Unicode input-character counts are also carried independently for models
whose catalog pricing uses `character_cost_per_1m`; tokens remain available for
rate limits and token-based budgets.
Exact processed-page counts are exposed separately as `input_pages` for models
whose catalog pricing uses `page_cost_per_1k`.
Exact processed-audio duration is exposed separately as
`input_audio_milliseconds` for models whose catalog pricing uses
`audio_cost_per_minute`.

`POST /v1/audio/speech` routes only to `audio_speech` capable deployments. It
accepts bounded text, voice, instructions, speed and audio format options,
applies the shared text policy, and returns a buffered binary response capped at
32 MiB. Character pricing uses the exact Unicode input count. The `audio`
stream format is supported; unsupported stream formats fail explicitly.

Native Mistral `POST /v1/audio/transcriptions` maps the supported language,
temperature, timestamp, diarization and context-bias fields, normalizes its
token usage, and settles provider-reported audio duration. WAV duration is
reserved exactly; compressed formats reserve the provider's 60-minute bound.

`POST /v1/search` routes only to explicitly `search` capable deployments. It
accepts a bounded string or list of queries plus result, domain, page-token and
country filters. Queries pass through the shared content policy; compatible
providers return at most 20 validated HTTP(S) results and billing settles one
search unit for each submitted query.

`POST /v1/ocr` routes only to explicitly `ocr` capable native Mistral
deployments. It accepts bounded HTTPS document/image URLs and inline base64 PDF
or image data, validates zero-based page selections and annotation formats, and
settles the reserved page count from the provider's exact `pages_processed`
usage. Inline binary data is sent to configured AV scanning; annotation prompts
pass through the normal text policy. Provider file references are rejected
until an owner-scoped Files API is available.

Anthropic Chat Completions may enable bounded native web retrieval with
`web_fetch_options`. The caller must supply one to twenty allowed domains and a
`max_content_tokens` limit; the gateway caps fetches at five, always enables
citations, requires an explicitly `web_fetch` capable deployment, includes the
tool configuration in TPM reserve, and disables exact and semantic response
caching for the request. The native `/v1/messages` route accepts the bounded
`web_search_20250305` and `web_fetch_20250910` tool forms and preserves their
validated server-tool result blocks in provider order. Streaming requests use a
bounded buffered response so output policy checks complete before the gateway
emits native SSE events.
Model catalog updates, deployment management and atomic model onboarding use
the same capability contract, including moderation, media, retrieval, prompt
cache and assistant-prefill capabilities.
Project details remain access-policy metadata rather than a synthetic billing
identity. The console joins owner teams, project access groups and their
virtual-key impact. Repeated `access_group_id` key-list query parameters use
server-side any-of matching and return an exact deduplicated total while rows
remain bounded by the normal page limit.
The console session endpoint validates the bearer credential through the normal
auth pipeline and returns only safe identity/scope metadata plus explicit UI
capabilities. It never echoes the bearer token or provider credentials.

The Playground uses the same authorized model list and inference endpoints as
external clients. It supports incremental Chat Completions and Responses SSE,
consumes the gateway's successful JSON fallback without replaying an inference,
supports request cancellation and a stable per-conversation session ID, and
keeps history only in tab memory. Responses continuity uses
`previous_response_id`; prompt, response, and stream-event content is never
written to browser storage.
