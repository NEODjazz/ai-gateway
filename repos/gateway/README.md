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
- `GET /admin/v1/request-logs`
- `GET /admin/v1/request-logs/{request_id}`
- `GET /admin/v1/request-logs/settings`
- `GET /admin/v1/policy-attachments`
- `PUT /admin/v1/policy-attachments/{id}`
- `DELETE /admin/v1/policy-attachments/{id}`

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

Guardrail policy definitions remain independent from their request scopes.
Policy attachments can apply an enabled DLP/AV policy globally or to the
intersection of configured team, virtual-key ID/alias, public model, and key-tag
patterns. Matching policies are combined with deployment guardrails and fail
closed if a required scanner is unavailable.
