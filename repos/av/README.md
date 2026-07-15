# AI Gateway - Antivirus Service

Antivirus microservice for provider-level malware checks through ICAP.

## Run

```powershell
go run ./cmd/av
```

## Endpoints

- `GET /healthz`
- `POST /scan`

## Configuration

```text
HTTP_ADDR=:8085
AV_ICAP_HOST=av.example.local
AV_ICAP_PORT=1344
AV_ICAP_SERVICE=/avscan
```

`AV_ICAP_PORT` is required and is not hardcoded by the service. The service also accepts generic `ICAP_*` variables as a fallback.
