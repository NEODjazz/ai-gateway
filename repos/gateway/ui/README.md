# AI Gateway admin console

The console is a route-based React application embedded in the gateway binary at
`/ui/`. It deliberately uses only documented gateway APIs. A bearer credential
is validated through `/admin/v1/session` before it is placed in
`sessionStorage`, so invalid values never open the console and the credential is
cleared when the browser tab closes.

The session response contains bounded identity/scope metadata and explicit
capabilities only. Global management routes require `admin`; the scoped team and
user directory is available to `team_admin`; Playground and API Reference remain
available to other authenticated credentials. Hidden navigation is a UX guard,
while backend RBAC remains authoritative for every operation. The sidebar shows
the authenticated user, roles, and optional team without exposing a token.

## Sidebar navigation

The sidebar has five labeled sections: **AI Gateway**, **Observability**,
**Access Control**, **Developer Tools**, and **Settings**. The navigation manifest
in `src/app/navigation.ts` is independent of route metadata and includes each
navigable page once. Existing URLs and router capability checks remain unchanged;
inaccessible links, empty submenus, and empty sections are omitted.

**Models & endpoints** contains Models, Model onboarding, Providers, Credentials,
Deployments, and Model groups. **Agentic** contains Agent profiles; **MCP** contains
MCP servers/toolsets; **Tools** contains Search/Tool policies; **Settings** contains
Router settings, Logging & alerts, and Single sign-on. Response cache and
Single sign-on are navigation labels for the existing `/cache` and `/settings`
routes. They do not change the page contracts.

Submenus start collapsed, except the group containing the current route. Boolean
disclosure preferences are stored under `ai-gateway.sidebar-collapsed.v1` in
browser local storage; they contain no identity, credential, or authorization data.
Malformed or unavailable storage falls back to defaults. A route change reopens
the active group, while a user can deliberately collapse it on the current page.
Child detail routes keep their parent link selected.

Gravity UI still provides the layout, logo, footer, and compact-mode control.
The sidebar list uses semantic headings, links, and disclosure buttons with
`aria-expanded`. Enter/Space toggles groups; Right/Down enters a group, Left closes
it, and Up/Down/Home/End move among children. In compact mode group buttons open
a Gravity UI popup with the same authorized links; Escape closes it and returns
focus. Both navigation modes scroll independently of the fixed account footer.

## Playground

The configuration rail controls Chat Completions and Responses requests. It
supports optional temperature and Top P, output limits, JSON object/schema
output, streaming and advanced JSON. Dedicated controls cannot be overridden by
advanced parameters; other fields are forwarded unchanged for authoritative
Gateway/provider validation. Empty optional values retain provider defaults.

Use the current UI session for this Gateway or apply an independent test virtual
key and optional base URL. Test keys stay in component memory. A custom URL
requires an explicit test key; console credentials and browser cookies are not
forwarded. A failed test key does not sign out the console. Model discovery uses
cancellation and generation checks to discard obsolete results.

Responses can continue with `previous_response_id` or browser conversation
history. Clear resets the session and continuity. Enter sends a message,
Shift+Enter inserts a newline, and Stop cancels the request. Failed responses
never become successful transcript turns or continuation IDs. Text output is
separate from reasoning and function-argument stream events.

Get code exports cURL, Python or JavaScript with `GATEWAY_API_KEY` as an environment
variable; it never includes the active credential. Response metadata displays
reported token usage and measured latency/first-token time. Missing usage stays
unavailable, and finalized costs remain in Usage & spend.

Image/PDF conversation uploads accept up to five files and 8 MiB total. Retained
browser history is limited to 40 turns and 32 MiB, with visible notification when
old pairs are dropped. Prompts are limited to 1 MiB, instructions to 64 KiB and
outgoing text requests to 24 MiB. Playground JSON, binary and stream reads have
explicit byte limits. Cost estimates require entered input/output rates and
reported usage, are labeled estimates and exclude non-token billing adjustments.

Compare runs up to three models in parallel with separate sessions, synchronized
or individual generation settings, per-panel pricing and errors, cancellation,
history and CSV results. Compliance runs policy checks without model generation,
with categorized suites, CSV import/export, at most three concurrent checks and
distinct allowed, blocked, failed and cancelled outcomes.

The endpoint selector also provides native Messages/Interactions and image,
embedding, speech, transcription, A2A and MCP forms. These construct their actual
public request dialects. Speech responses stay binary; MCP execution requires an
explicit click and a unique idempotency key. Code exports preserve these details
and offer a bounded preview plus a complete download.

The complete implementation requirements and pending work are tracked in
[the Playground functional plan](../../../docs/playground-functional-plan.md).

## Dependency override

The lockfile pins the navigation package's codemod dependency to `jscodeshift 17.4.0`.
This version replaces the vulnerable `micromatch → braces` dependency chain
(GHSA-vfj7-8cjw-p6xm) with `picomatch`; runtime Gravity UI component versions remain
unchanged. The gateway does not execute navigation codemods. Keep the scoped
override until the navigation package updates its dependency, and retain
`npm audit --omit=dev --audit-level=high` in CI.

A standalone TSX codemod dry run validates the new jscodeshift runner. The packaged
navigation `v4` codemod cannot be validated: its npm tarball omits the referenced
`codemods/utils` files. This existing packaging limitation is independent of the
override and does not affect the console bundle.

## Development

```sh
npm ci
npm run typecheck
npm test
npm run test:coverage
npm run build
```

`npm run build` writes the production bundle to
`../internal/gateway/adminui`, where Go embeds it. The gateway serves the same
HTML shell for `/ui/*` deep links and returns `404` for unknown asset paths.

The route manifest contains 36 dashboard destinations. Routes backed by existing
gateway APIs are fully interactive. Search tools and executable skill content are
visibly marked unavailable instead of showing mock data or pretending that
persistence and enforcement exist.

The Logs workspace combines request and audit events. Request-log view, preset
or custom date-time window (bounded to 90 days), applied filters, and the selected request detail are URL state, so an
operator can share a diagnostic link or use browser back/forward without losing
the investigation. For example,
`/ui/logs?view=sessions&days=30&team_id=platform` restores the server-aggregated
session view, while `log=<request-id>` opens the bounded metadata-only detail.
No prompt, response, raw provider error, bearer token, or provider credential is
placed in the URL or returned by the log APIs.

Usage keeps public models, concrete upstream models, logical providers, and
routed endpoints as separate dimensions. This makes alias behavior and routing
decisions visible without creating duplicate catalog records or attributing a
deployment name to the provider column. Each dimension row has an Inspect action
that loads currency-safe totals and daily activity from the server using the
current period and filters; owner drill-downs use non-secret key fingerprints or
configured organization, team, and user IDs.
Overview cards, breakdown tables, drill-downs, CSV export, request rows, and
session/trace aggregates expose provider-reported cache-read and cache-write
input tokens separately from proxy cache hits.

Virtual-key create/edit uses searchable chip multi-selects for both direct
model grants and enabled Access Groups. The key table exposes assignments as a
selectable column. Group grants are runtime policy, not descriptive UI metadata:
gateway unions the selected groups and intersects that result with the key's
direct model/tool grants, failing closed for missing or disabled assignments.
The row action opens `/api-keys/:id`, which keeps identity, grants, lifecycle,
currency-separated usage, daily activity and all applicable budget policies in
one route-based workspace. Rotate keeps its one-time secret disclosure and copy
confirmation; settings edits reuse the configured organization/team/user and
model selectors from the catalog.

Access Groups use dedicated `/access-groups` and `/access-groups/:id` routes.
The detail view shows current model/tool grants, project ownership, attached
virtual keys, non-revoked impact and per-key budget projections. Create and edit
forms select configured projects, models and MCP toolsets while retaining an
explicit custom-grant path for wildcard policies. Deletion remains unavailable
until every non-revoked key assignment is removed.

Budgets use a dedicated management page rather than generic CRUD. It presents
live cost and token progress, reset timestamps, risk/exhaustion state, explicit
currency, scope filtering, configurable columns and virtual-key-style actions.
Create/edit loads the target from the selected configured registry and omits an
unused optional limit instead of serializing it as zero.

Logging & Alerts is a dedicated callback workspace with current-replica delivery counters,
content-boundary disclosure, searchable/configurable columns and Test/Edit/Delete
actions. Probe latency is shown without sending inference content. Bearer
secrets are write-only: an empty edit preserves the existing encrypted value.

Caching replaces the raw diagnostics payload with current-replica exact and
semantic health cards plus an operation/result table. The page explains the
boundary between gateway response-cache hits and provider prompt-cache tokens,
and keeps TTL/capacity/Redis configuration read-only and deployment-managed.

MCP Servers and MCP Toolsets use dedicated management pages rather than generic
CSV CRUD forms. Server creation suggests the canonical
`mcp:<server-label>@<https-url>` grant, toolsets select grants from configured
servers, and both tables expose reference counts. Assignment details identify
Access Groups and non-secret virtual-key IDs. Delete actions are protected by
server-to-toolset and toolset-to-key/group impact checks. The console does not
collect MCP OAuth tokens or claim direct network health because MCP execution
remains provider-mediated in this gateway architecture.

Guardrail Monitor is a dedicated overview and module drill-down workspace. It
uses server-side time, policy, source, outcome and DLP/AV filters; shows pass
rate, blocked/unavailable evaluations, latency, policy impact and timeline
buckets; and keeps event rows metadata-only. The page identifies whether the
report comes from bounded Redis history shared across replicas or from the
current-replica fallback, including retention saturation and store errors.

Teams use a route-based `/teams/:id` workspace. It resolves configured users,
membership-specific roles and safe team-owned virtual-key metadata without raw
ID entry. Global administrators can add visible configured users; matching
team administrators can inspect and update their scoped memberships. Role
changes and removals go through audited, team-scoped gateway endpoints.

Organizations use a route-based `/organizations/:id` workspace. It joins the
configured team registry with organization-owned virtual keys, offers audited
assignment and removal actions, and excludes teams owned by another
organization. Reparenting is intentionally explicit: remove the current
assignment before selecting the team for a different organization.

Comparison sends shared image/PDF attachments to each selected model and retains
typed history independently. Model refusals are displayed as output. Structured
tool calls, safe output images and URL citations are shown separately; copy
controls report clipboard failures. Native Messages/Interactions conversations
retain bounded visible history. Speech download filenames use the returned MIME
type rather than the current form setting.
