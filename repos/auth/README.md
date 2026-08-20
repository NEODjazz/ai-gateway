# AI Gateway - Auth Service

Auth microservice for API keys, JWT validation, user identity, and roles.

## Run

```powershell
go run ./cmd/auth
```

## Endpoints

- `GET /healthz`
- `POST /authorize`

`POST /authorize` accepts only `{ "token": "..." }` and returns `user_id`,
`roles`, and an irreversible `credential_id`. The bearer token is never echoed
in the response or forwarded to another module.

## JWT

Supported algorithm: `HS256`.

Environment:

```text
AUTH_JWT_SECRET=dev-jwt-secret
AUTH_JWT_ISSUER=ai-gateway
AUTH_JWT_AUDIENCE=ai-gateway
```
