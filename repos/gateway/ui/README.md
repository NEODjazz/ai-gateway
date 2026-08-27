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
gateway APIs are fully interactive. Search tools, skills, cross-resource policies,
central tag management, and dynamic router settings are visibly marked unavailable
instead of showing mock data or pretending that persistence and enforcement exist.
