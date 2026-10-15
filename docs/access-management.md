# Access-control directory

**Access Control → Users / Teams** provides server-side directory search, status
filters, stable sorting, configurable columns and bounded pagination. Users can
also be filtered by exact approved global role. Counts describe the complete
authorized result, including records beyond the first hundred. Search is a
case-insensitive literal substring; `%` and `_` are ordinary characters.

The UI excludes provisioned deletion tombstones, distinguishes loading from an
empty result, ignores late requests and identifies retained pages after a failed
refresh. Session changes hide previous results and drafts. A mutation completing
for an older session cannot close a newly opened form.

Platform administrators can create and edit directory users and teams. IDs are
generated in the form and remain editable during creation; they cannot be
changed when editing. User approvals use structured role choices. Existing
custom approvals and external IDs are retained when editing another field.
Disabling an active identity asks for confirmation. These are directory actions:
they do not send invitation email, issue a virtual key or reset an IdP password.
Browser sign-in still requires the configured SSO connection and an exact
approved principal binding. Team and organization administrator approvals still
require the corresponding memberships.

Matching team administrators retain their scoped team management and read-only
user directory. The server pins the verified team scope even if a caller supplies
another `team_id`. A scoped user detail read exposes only memberships visible in
that team. Organization administrators do not gain platform directory access.

## API and upgrade compatibility

Existing directory GET and PUT routes remain available. GET list routes add
`search`, `status`, `include_deleted`, `sort_by`, and `sort_order`; users also add
`role`. The default for API `include_deleted` remains `true`. List responses keep
`data` and `total` and add `offset` and `limit`. Page size remains 1–500.

Sort keys are `id`, `displayName`, `externalId`, `status`, `createdAt`,
`updatedAt`, and, for users, `userName`. A sort requires `sort_order=ascending`
or `descending`. The identity is a stable tie-breaker. Page rows and total are
read in one PostgreSQL repeatable-read snapshot.

New `POST /admin/v1/users` and `POST /admin/v1/teams` create identities without
overwriting an occupied ID, returning 201 or 409. They are platform-admin only.
Memberships are changed through the existing membership API. New
`GET /admin/v1/users/{id}` enforces the verified directory scope.

Upgrade **Auth before Gateway**. Filtered list responses from Auth advertise
`filtering_version: 1` internally. Gateway refuses search/status/role results from
an older backend rather than displaying an unfiltered page as filtered data.
Unfiltered legacy calls retain their existing behavior. No migration or new
dependency is needed for these directory controls.

## Identity workspaces

Open a user or team name to inspect its workspace. Overview combines identity
metadata, memberships and scoped virtual keys; Members/Teams, Virtual keys and
Settings tabs provide focused views. Each section loads independently, so a key
inventory failure does not hide the profile or memberships. Late responses and
completed mutations from a previous identity are discarded. Team metadata uses
`GET /admin/v1/teams/{id}` and a count query rather than loading every member ID.

Membership search and pagination use `GET /admin/v1/team-memberships`, bounded to
1–500 records per page and requiring a team or user filter for platform admins.
Team administrators are pinned to their verified team, including reverse user
lookups. Display names, email, statuses and membership roles come from the same
PostgreSQL snapshot as the count. Legacy membership list routes remain available.

Adding a member uses `POST /admin/v1/teams/{id}/members`. An existing membership
returns 409 and preserves its roles. Creation checks that both identities are
active and not deleted. Role edits and removal retain the existing PUT/DELETE
routes. User and team workspaces use the same membership operations. Selection
forms search and paginate the authorized directory; they do not require a raw ID
and do not preload the first 500 identities. Missing directory access is not
replaced with global discovery. Scoped sessions do not load global key inventory.

Settings retain immutable IDs, external identity metadata and global approvals.
Membership roles are edited separately. Key tables show declared model patterns
and per-key RPM/TPM; they do not present those as aggregate team quotas or claim
that key patterns override other effective policy. Budget management and IdP
binding links lead to their existing administrative workspaces.

## Identity budgets

User and team workspaces include a Budgets tab and a scoped overview section.
Only global administrators can read or mutate financial controls. Scoped sessions
render an access explanation without requesting global or identity budget data.
Creating a policy pins its user/team scope to the current workspace; no directory
lookup or editable scope field is needed. Editing and disabling use the existing
audited budget management routes and preserve the billing enforcement path.
A failed save retains the draft. The dialog cannot be dismissed or resubmitted
while saving. Navigation/session changes discard stale data and mutation results.

`GET /admin/v1/budgets/page` adds a bounded list of policies assigned directly to
one identity, with current usage and exact totals. It requires `scope_type=team`
or `user` and an exact `scope_id`, and accepts `offset` (default 0) and `limit`
(default 25, maximum 100). Policies are ordered by ID; the count, page and usage
share one PostgreSQL repeatable-read snapshot. Amounts include committed usage
and live reservations, isolated by currency and reset window. Canceled, expired
and older-window reservations do not inflate current spend. Disabled policies
remain visible. Each row shows its actual caps, remaining amounts and UTC reset.
Missing or invalid summaries produce an error rather than zero spend.

These direct policies do not describe every applicable cap: global, organization,
key, model, provider, deployment and request-tag policies remain independent.
No aggregate financial total mixes different currencies or overlapping windows.
Upgrade **Billing before Gateway** to enable the new `/internal/v1/budgets/page`
route. An older backend fails visibly; Gateway does not fall back to loading or
filtering the global budget inventory. Existing list/mutation APIs are unchanged;
no database migration or new dependency is needed. Required PostgreSQL CI tests
include identity budget paging and usage correctness on the real test database.

## Identity models and aggregate quotas

The **Models & limits** tab in a user/team workspace manages additional inference
restrictions. A global administrator can save or reset them. A matching team
administrator can inspect their team's policy but cannot raise its limits or
change its models. Scoped sessions never load another user's global policy.
Existing directory names, roles, memberships, principal bindings, virtual keys
and budget policies are unchanged by these settings.

`GET`, `PUT` and `DELETE /admin/v1/identity-policies/{scope}/{id}` use `team` or
`user` scope. GET/PUT require an existing directory identity without a deletion
tombstone. DELETE can clean up an orphaned policy. PUT is a complete replacement,
requires `model_access`, `rpm` and `tpm`, and accepts `allowed_models`:

- `inherit`: no additional model restriction; other grants remain authoritative.
- `allowlist`: 1–100 exact model IDs or trailing-wildcard patterns, at most 512
  bytes each. A selected empty list is rejected, never interpreted as all models.
- `deny`: block every model. Model restrictions intersect the credential,
  selected-team, user, access-group and tag grants; no policy grants new access.

The UI uses configured model choices, retains existing explicit patterns if the
catalog fails, and distinguishes unrestricted extra policy from an explicit deny.
Only actual scope policy is displayed, not a fabricated union of all keys' grants.
Model discovery and fallback selection apply the same restrictions as inference;
queued work refreshes the current policy before execution. An open Realtime
session rechecks model access before admitting each new response. The effective policy
also participates in exact and semantic cache isolation. Failed reads are closed
before model discovery or quota admission. Forms retain failed drafts and reject
stale loads/mutations after navigation or verified-session changes.

RPM/TPM are aggregate quotas per exact verified user ID and per selected team ID,
shared across credentials. Team membership alone does not select a team: the
verified key, principal binding or browser session must bind the request to it.
User caps apply across team contexts. Zero adds no quota for that dimension;
credential, global, provider/deployment and budget limits remain independent.
Accepted ranges are RPM 0–10000000 and TPM 0–1000000000. TPM uses the existing
conservative input-plus-maximum-output reservation, including tool context and
preprocessed documents. Fixed one-minute windows are shared through Redis;
memory counters apply per process. Changes do not reset an existing window.
Admission checks credential, user, selected-team, then global quotas. A rejection
by a later quota may retain earlier reservations; failures are not refunded.
HTTP 429 includes Retry-After; observed storage failures return 503.

Policies persist in the existing PostgreSQL admin-state snapshot, with optimistic
revision checks and rollback on a failed save. The registry is bounded to 10000
identity policies; existing policies remain editable at capacity. Without the
control plane, configuration is process-local and is lost on restart. All
replicas need shared Redis and the same control-plane configuration. Replica
updates follow the existing control-plane refresh cadence. In-flight requests
retain their already performed authorization checks.

**Upgrade all Gateway replicas before activating identity policies.** A snapshot
containing these policies uses admin-state schema version 4; older supported
snapshots remain readable. Version 4 prevents older Gateway runtimes from silently
ignoring new restrictions. Activating a policy during a mixed-version rollout can
make older replicas reject requests. No new dependency or database migration is
required. Regression coverage includes API scope, key-independent counters on
memory/Redis, concurrent admission, cache isolation, and real PostgreSQL restore,
replica refresh and quota enforcement. Existing financial and membership APIs
retain their compatibility requirements described above.

## Team organization ownership

Team workspaces expose an **Organization** tab and ownership controls in Overview
and Settings. Global administrators can assign an unowned team or explicitly
unassign its current organization. Matching team administrators can inspect only
their team's owner; they cannot transfer it or load global organization choices.
An unknown or failed ownership read is never presented as an unassigned team.
The previous successful read is identified after refresh failures, and changing
identity/session invalidates pending reads, forms and mutation results.

New team forms optionally select an organization. A nonempty `organization_id`
in `POST /admin/v1/teams` creates the team and its binding in one PostgreSQL
transaction. A missing/disabled organization, occupied ID or other failure rolls
back the entire creation. Without this field, the existing creation path remains
unchanged. The success notice links to the identity workspace for memberships,
models, quotas and budgets. Existing team settings edit directory metadata;
ownership changes use the separate confirmed operation.

`GET /admin/v1/teams/{id}/organization` returns one `team_id` and either bounded
organization metadata or explicit `organization: null`. It does not load the
organization inventory or unrelated team IDs. `PUT` and `DELETE` on that route
require an exact `organization_id` body and global administrator authorization.
Assignment requires a nondeleted team and active organization. The unique team
ownership constraint prevents implicit re-parenting, including concurrent
requests. DELETE is conditional on the supplied previous owner; a stale owner ID
cannot remove another organization's binding. The UI offers assignment only for
an unassigned team and confirms unassignment before allowing a different owner.

Ownership does **not** rewrite virtual keys, directory approvals, principal
bindings or their explicit organization scopes. A key pinned to the old
organization and this team fails authentication after their relationship is
removed. A team-only key does not automatically acquire an organization ID.
Operators must configure request scopes explicitly through the existing key and
principal management controls. Budgets retain their existing scope semantics.

`GET /admin/v1/organizations/page` provides global-admin organization choices,
ordered by case-insensitive name then ID, with `search`, `status`, `offset` and
`limit` (default 25, maximum 100). Search is a literal case-insensitive substring;
rows and exact count share one repeatable-read snapshot. The picker requests
active organizations, searches the complete directory, pages beyond 500 and
retains an explicit choice across queries. Legacy organization list routes and
response shapes remain available. Their assignment return lookup now addresses
the exact organization instead of scanning the first 500 rows.

Upgrade **Auth before Gateway** for organization choices, ownership controls and
atomic creation with an organization. An older backend fails visibly; the UI does
not fall back to loading a partial global inventory or creating an unowned team.
No new migration or dependency is needed. PostgreSQL regression tests cover
paging, exact ownership, rollback, concurrent assignment and unchanged key scopes.
