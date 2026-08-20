# AI Gateway - Auth Service

Auth microservice for API keys, JWT validation, user identity, and roles.

## Run

```powershell
go run ./cmd/auth
```

## Endpoints

- `GET /healthz`
- `GET /livez`
- `GET /readyz`
- `POST /authorize`

`POST /authorize` accepts only `{ "token": "..." }` and returns `user_id`,
`roles`, and an irreversible `credential_id`. The bearer token is never echoed
in the response or forwarded to another module.

## PostgreSQL virtual keys

Set `AUTH_POSTGRES_KEYS_ENABLED=true`, `AUTH_POSTGRES_DSN`, and a separate
`AUTH_KEY_HASH_SECRET` to authorize revocable virtual keys from PostgreSQL.
Tokens are looked up by a full HMAC-SHA256 value; the database stores no bearer
plaintext, and the authorization response exposes the opaque key ID as
`credential_id`. Migration `003_virtual_keys.sql` adds expiration, revocation,
last-used tracking, and linked rotation families.

`AUTH_STATIC_KEY_FALLBACK_ENABLED` controls migration fallback to
`AUTH_VIRTUAL_KEYS_JSON`. `AUTH_DEMO_KEYS_ENABLED` controls the two built-in demo
keys. Set both to `false` in production after persistent keys have been loaded.
The service fails closed on a PostgreSQL lookup error; `/readyz` verifies both
the connection and the migration.

## JWT

Supported algorithm: `HS256`.

Environment:

```text
AUTH_JWT_SECRET=dev-jwt-secret
AUTH_JWT_ISSUER=ai-gateway
AUTH_JWT_AUDIENCE=ai-gateway
AUTH_POSTGRES_KEYS_ENABLED=true
AUTH_POSTGRES_DSN=postgres://ai_gateway:password@postgres:5432/ai_gateway
AUTH_KEY_HASH_SECRET=separate-random-pepper
AUTH_STATIC_KEY_FALLBACK_ENABLED=false
AUTH_DEMO_KEYS_ENABLED=false
```
