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

Delegated organization management UI, verified tenant selection, remaining
per-resource permission checks, and multiple connections remain separate
implementation stages; the role does not confer global configuration or budget
mutation access.
