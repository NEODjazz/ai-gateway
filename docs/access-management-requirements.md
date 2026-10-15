# Team and user management

The access-control console must provide a directory workspace for teams and
users. Directory identity, membership roles, inference grants and billing policy
remain distinct; changing a display name must not change authorization.

## Directory lists

- Teams: name and ID, organization, member count, model access, virtual keys,
  spend/budget, status and creation date; inspect and edit actions.
- Users: ID, name, email, status, approved global roles, teams, external identity,
  keys, spend/budget and creation/update dates; inspect and edit actions.
- Search, filters, sorting, configurable columns and pagination operate over the
  complete authorized directory. Totals come from the backend rather than a
  fetched page. Page size is bounded, and sorting has a stable identity tie-breaker.
- Errors preserve the previous valid view with a visible error state. Changes to
  filters, identity or page invalidate delayed requests. Empty and loading states
  remain distinguishable.

## Teams

- Creation and editing collect name, optional ID, description and status, with
  organization assignment and links to effective models, quotas and budgets.
- A team workspace exposes overview, members, keys, settings and financial
  controls. Member roles belong to that membership, not to the global user.
- Add configured users, edit membership roles and remove memberships. Large
  directories must not make members beyond the first page inaccessible.
- Models and limits shown in the workspace must describe enforced policy. A
  team-level setting must not be implemented as an unrelated per-key default.
- Keep organization ownership explicit; assigning a team must not silently
  re-parent it from another organization.

## Users

- Create and update directory identities using explicit approved roles and
  status. Provide a structured role selector rather than a free-form role list.
- A user workspace exposes overview, settings, team memberships, owned keys and
  budgets, with links to identity-provider principal bindings and effective grants.
- Manage team membership from the user workspace using the same scoped membership
  API as the team workspace. Display actual membership roles.
- Onboarding and bulk onboarding must show per-user outcomes. Do not claim an
  invitation was emailed or a password reset was performed unless a configured
  identity backend actually performed that operation. Identity-provider sign-in
  and exact subject approvals remain authoritative.
- Disabling an identity must use the existing authentication enforcement path,
  with a clear distinction from removing a membership or revoking a virtual key.

## Permissions and reliability

Only platform administrators edit global user approvals. Matching team
administrators manage their team within the existing boundary; organization
administrators must not gain platform-wide directory or key access. Global
financial reports and key inventories must not be fetched by scoped sessions.
No plaintext credentials are loaded into directory tables or CSV exports.

Forms must contain keyboard focus, support cancellation, preserve drafts on a
failed save and prevent duplicate submissions. Confirm destructive operations,
surface partial failures, and refresh only data belonging to the current scope.

Global inference quota is a separate setting in **Settings → Router settings**;
its defaults are 60 RPM / 100000 TPM. It limits aggregate traffic across all
identities and does not imply a new directory role or membership grant.

Verification must exercise list paging beyond the first page, scoped access,
creation/editing, membership changes in both workspaces, failed saves, stale
loads, keyboard navigation and real rendered layouts. Backend policy changes
also require PostgreSQL integration coverage and concurrency checks where relevant.
