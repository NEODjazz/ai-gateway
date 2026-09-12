# AI Gateway - Antivirus Service

Antivirus microservice for provider-level malware checks through ICAP.

## Run

```powershell
go run ./cmd/av
```

## Endpoints

- `GET /healthz`
- `GET /readyz`
- `POST /scan`

`POST /scan` accepts `request_id`, a text projection in `content`, and optional
validated image `attachments` containing `media_type` and `data_base64`. It does
not receive bearer credentials, identity, or the complete gateway context. Text
and every decoded attachment are separate ICAP scans. JPEG, PNG, GIF, and WebP
are supported with signature checks and bounded count/size.

## Configuration

```text
HTTP_ADDR=:8085
AV_ICAP_HOST=av.example.local
AV_ICAP_PORT=1344
AV_ICAP_SERVICE=/av
AV_ICAP_TIMEOUT=5s
```

`AV_ICAP_HOST` and `AV_ICAP_PORT` must identify a reachable ICAP server for
non-empty scans. `/healthz` reports process liveness. `/readyz` sends a
content-free ICAP `OPTIONS` request and returns 503 while the configured service
is unavailable. The service does not fail startup when settings are empty;
readiness and scan requests then fail with a dependency error. Generic `ICAP_*` variables are
accepted as fallbacks. `AV_ICAP_SERVICE` defaults to `/av` and
`AV_ICAP_TIMEOUT` to `5s`.
