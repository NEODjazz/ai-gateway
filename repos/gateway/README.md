# AI Gateway - Gateway

Central OpenAI-compatible gateway with provider routing, failover, auth integration, and provider-level module execution.

## Run

```powershell
go run ./cmd/gateway
```

## Selected endpoints

The complete route and schema reference is [OpenAPI](api/openapi.yaml).
For MCP registry management, client-side tools, Responses passthrough and
permission examples, see [MCP integration](../../docs/mcp.md).

- `GET /healthz`
- `POST /v1/chat/completions`
- `POST /v1/responses`
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
`POST /admin/v1/policy-attachments/resolve` performs a metadata-only dry
resolution with the exact runtime matcher. The Policies workspace uses it to
show matched attachments, cumulative DLP/AV requirements, and fail-closed
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
