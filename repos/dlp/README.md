# AI Gateway - DLP Service

DLP microservice for provider-level content leak checks through ICAP.

## Run

```powershell
go run ./cmd/dlp
```

## Endpoints

- `GET /healthz`
- `POST /scan`

## Configuration

```text
HTTP_ADDR=:8084
DLP_ICAP_HOST=dlp.example.local
DLP_ICAP_PORT=1344
DLP_ICAP_SERVICE=/reqmod
```

`DLP_ICAP_PORT` is required and is not hardcoded by the service. The service also accepts generic `ICAP_*` variables as a fallback.
