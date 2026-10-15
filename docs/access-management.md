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
