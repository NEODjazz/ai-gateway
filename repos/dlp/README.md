# AI Gateway - DLP Service

DLP microservice for provider-level content leak checks through ICAP.

## Run

```powershell
go run ./cmd/dlp
```

## Endpoints

- `GET /healthz`
- `GET /readyz`
- `POST /scan`

`POST /scan` accepts only `request_id` and a text projection in `content`; it
does not receive bearer credentials, identity, or the complete gateway context.

## Configuration

```text
HTTP_ADDR=:8084
DLP_ICAP_HOST=dlp.example.local
DLP_ICAP_PORT=1344
DLP_ICAP_SERVICE=/dlp
DLP_ICAP_TIMEOUT=5s
```

`DLP_ICAP_HOST` and `DLP_ICAP_PORT` must identify a reachable ICAP server for
non-empty scans. `/healthz` reports process liveness. `/readyz` sends a
content-free ICAP `OPTIONS` request and returns 503 while the configured service
is unavailable. The service does not fail startup when settings are empty;
readiness and scan requests then fail with a dependency error. Generic `ICAP_*` variables are
accepted as fallbacks. `DLP_ICAP_SERVICE` defaults to `/dlp` and
`DLP_ICAP_TIMEOUT` to `5s`.
