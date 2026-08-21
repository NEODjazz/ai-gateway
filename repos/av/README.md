# AI Gateway - Antivirus Service

Antivirus microservice for provider-level malware checks through ICAP.

## Run

```powershell
go run ./cmd/av
```

## Endpoints

- `GET /healthz`
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
AV_ICAP_SERVICE=/avscan
```

`AV_ICAP_PORT` is required and is not hardcoded by the service. The service also accepts generic `ICAP_*` variables as a fallback.
