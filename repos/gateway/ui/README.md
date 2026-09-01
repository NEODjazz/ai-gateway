# AI Gateway admin console

The console is a route-based React application embedded in the gateway binary at
`/ui/`. It deliberately uses only the gateway's documented admin APIs and keeps
the admin bearer token in `sessionStorage`, so it is cleared when the browser tab
is closed.

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

The Logs workspace combines request and audit events. Request-log view, time
window, applied filters, and the selected request detail are URL state, so an
operator can share a diagnostic link or use browser back/forward without losing
the investigation. For example,
`/ui/logs?view=sessions&days=30&team_id=platform` restores the server-aggregated
session view, while `log=<request-id>` opens the bounded metadata-only detail.
No prompt, response, raw provider error, bearer token, or provider credential is
placed in the URL or returned by the log APIs.
