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
