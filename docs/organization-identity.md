# Organization identity and approvals

An organization is a tenant identity boundary. The authenticated directory
binding determines `organization_id`; a public request header, selected UI label,
or IdP claim cannot assign it. Global `admin` remains a platform role.

## Explicit membership

A global administrator can use these management endpoints:

- `GET /admin/v1/organizations/{id}/members?limit=100&offset=0`
- `PUT /admin/v1/organizations/{id}/members/{user_id}`

The PUT body contains `status` (`active` or `disabled`) and `roles` (a nonempty,
unique subset of `user`, `developer`, `org_admin`). Path IDs take precedence over
body IDs. The list includes disabled approvals and reports the actual total
independently of pagination. Mutations require the audit service when configured.
Organization administrators cannot approve their own membership or platform roles.

Example approval body:

```json
{"status":"active","roles":["user","org_admin"]}
```

Browser OIDC and directory API JWT role mappings may target `org_admin`. This
requires an active approval for the verified user in the principal's organization;
a global directory role or an IdP claim alone is insufficient. Ordinary user and
developer roles also intersect explicit membership roles when an approval exists.
Existing active team membership remains sufficient for ordinary roles when no
explicit organization approval exists. A disabled explicit approval denies access
even if a team membership remains active. No migration grants `org_admin`.

Persistent virtual keys containing `org_admin` must identify the approved user
and organization. Each authentication checks the current approval. Static keys
and legacy claim-only JWT cannot provide `org_admin`.

## Immutable tenant ownership

JWT principal policies now expose an optional `organization_id`. When a team is
specified, its organization is inferred on creation and must match an explicit
organization value. Direct organization bindings without a team require explicit
active organization membership. The user and organization of a saved binding are
immutable, including while disabled. Use a separately approved issuer/client
binding to establish another tenant context; changing a team cannot transfer the
existing identity's tenant or resource ownership.

Removing or reassigning the principal's team from its pinned organization denies
authentication. It does not turn the identity into an unscoped principal. Server
sessions pin the tenant at login and recheck current membership and role approval
on every request. Durable jobs recheck their approved policy and deny stale grants.

## Migration and compatibility

Auth migration `016_organization_memberships.sql` creates the approval table and
pins existing team-derived principal organizations once, when adding the column.
Migration replay never reassigns later unscoped bindings. No credentials, files,
or billing records are deleted or reassigned. Apply the rendered PostgreSQL chart
migrations before updating Auth; readiness checks require the new table/column.

Tenant-bound server sessions created before this version require a fresh login,
because their encrypted records do not yet pin an organization. API issuer trust
is unchanged. Existing unscoped identity bindings do not automatically adopt an
organization when their team is later assigned: establish a new explicit binding.

## Organization reports

Verified `org_admin` may read `/admin/v1/usage/report`, its own
`/admin/v1/customers/organization/{id}/usage`, request-log lists/groups/detail,
and retention metadata. Gateway always supplies the authenticated organization to
Billing. Billing independently constrains the query using service-authenticated
actor metadata. Public actor/organization headers cannot override it. Conflicting
organization filters and user/team/key customer scopes return 403; the latter are
not a substitute for a tenant filter when identities span organizations.

Report totals, grouped data, pagination, and CSV input therefore share the same
tenant predicate. Single-event detail also filters organization in the storage
query; foreign and unassigned events return 404. A backend without scoped detail
support returns 503 rather than falling back to global lookup. Platform `admin`
retains its existing global reporting contract.

## Virtual keys

Verified `org_admin` can list safe metadata only for keys whose explicit
`organization_id` matches its identity. Auth applies the same predicate to the
page and total. A user's membership in multiple organizations or a team match
does not expose foreign/unscoped keys. Gateway checks returned ownership and
refuses a backend without scoped pagination. Global platform-admin filters keep
their existing affiliation search behavior.

Key mutations and financial expansion remain platform-admin operations. Auth also
rejects delegated mutation attempts on the authenticated service channel. Update
and rotation now require the original user and organization, including an empty
organization. Rotation rejection leaves the original credential valid. To grant
another tenant or user access, create a separate key. This intentionally tightens
the previous management contract that permitted ownership edits.

For an organization-owned key with a team, the team must belong to that
organization on create/update/rotate and authentication. Removing or reassigning
the team denies the existing key; it cannot transfer its tenant. Existing invalid
organization/team pairs require an operator to correct the team in the same
organization. No migration silently assigns user-only or unscoped keys to a tenant.

File/job owner namespaces continue to use the authenticated user and credential.
Pinning identity ownership prevents tenant transfer of those namespaces while
preserving existing data. Cache scopes additionally include the authenticated
organization and effective policy. This does not grant organization administrators
access to another user's files or jobs.

The console exposes organization reports and read-only key metadata through
optional `organization_capabilities` (`organization_reports`, `organization_keys`)
in `/admin/v1/session`; the existing `capabilities` response enum remains unchanged.
They require
both verified `org_admin` and a pinned organization. The role does not confer
platform configuration, directory approval or budget mutation access.

## Permission and resource boundary

| Surface | Organization administrator contract | Enforcement / regression evidence |
| --- | --- | --- |
| Provider credentials, providers, deployment/model configuration | Platform administration only | `organization_permissions_test.go` covers every published platform management route, including forged actor headers |
| Inference model catalog | Current principal's approved model grants; no credential material | Existing model authorization and catalog tests |
| Organization/team membership, principal bindings, SSO settings and API issuer trust | Platform approvals; no self-elevation through `org_admin` | Endpoint permission matrix and organization principal PostgreSQL regressions |
| Virtual keys | Explicit tenant-owned metadata only; mutations/financial expansion require platform admin | Strict page and total predicates, immutable user/tenant, PostgreSQL update/rotation regressions |
| Files, skills, vector stores, containers, conversations | Personal credential ownership within the fixed tenant identity; `org_admin` does not bypass ownership | Owner-scoped lifecycle tests; same-user separate-tenant credential file read/content/list/delete regression |
| Responses, batches, fine-tuning, videos, assistant runs, A2A | Credential-owned lifecycle; workers reauthorize current pinned identity and grants | Resource lifecycle/foreign-owner tests and durable principal reauthorization regressions |
| Usage, request logs and CSV input | Authenticated organization predicate on Gateway and Billing | Forced scopes, foreign filters rejected, storage-scoped detail and grouped totals tests, real ClickHouse isolation integration |
| Budgets, billing administration and logging/export destinations | Platform administration only; organization spend reports remain scoped | Platform endpoint matrix; budget enforcement and organization attribution PostgreSQL tests |

Resource ownership remains based on `credential + user` for compatibility. This
is safe under the enforced immutable principal/key tenant contract: one credential
cannot be moved into another organization. A second tenant requires a separate
approved issuer/client principal or key and therefore another resource namespace.
Changing headers or selecting an organization label cannot switch ownership.
There is no cross-tenant resource sharing or automatic migration of existing files
and jobs. A future same-credential context switch would require a separately
versioned owner namespace and explicit migration; it is not supported here.

## Organization console and approvals

Platform administrators manage explicit membership in **Organizations → an
organization → Organization approvals**. Choose an existing user, active/disabled
membership and the subset `org_admin`, `user`, `developer`. Mapping an IdP group
cannot create or expand approval. The list uses actual server totals and paging;
foreign membership rows are rejected by the UI.

Organization administrators can open Usage & spend, request logs and read-only
Virtual keys. Key pages use explicit organization ownership and do not request
global directory or financial expansion. Global audit events and key mutations
remain hidden. Usage/CSV data is constrained on the server; child user/team/key
breakdowns are read-only because those drill-down scopes cannot replace the
organization predicate. Request logs pin organization, skip global directory
lookups, and support exact child IDs within the same server-enforced predicate.
Late responses from old log windows cannot overwrite current data.

The sidebar displays the verified organization. Switch organization / connection
starts a fresh OIDC login from an enabled connection and clears the previous tab
bearer credential, so it cannot override the new HttpOnly browser session after
callback. The selected label does not grant membership or mutate API credentials.
