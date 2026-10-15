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
