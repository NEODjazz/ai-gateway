# AI Gateway - Gateway

Central OpenAI-compatible gateway with provider routing, failover, auth integration, and provider-level module execution.

## Run

```powershell
go run ./cmd/gateway
```

## Endpoints

- `GET /healthz`
- `POST /v1/chat/completions`
- `POST /v1/responses`

Gateway-level module:

- `auth`

Provider-level modules:

- `anonymizer`
- `dlp`
- `av`
- `billing`

The internal `RequestContext` is not serialized between services. Each remote
module has a minimal typed contract; only auth receives the client bearer token,
which gateway clears before provider-level processing.
