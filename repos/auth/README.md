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
last-used tracking, and linked rotation families; `004_allowed_tools.sql` adds
credential-level exact/wildcard tool grants; `005_virtual_key_metadata.sql`
adds alias, description, tags, and reversible disable state. Disabled keys fail
authorization without losing their policy or rotation history.
Migration `006_identity_directory.sql` adds users, teams and scoped membership
roles. A disabled directory user or team also disables its persistent keys.

`AUTH_STATIC_KEY_FALLBACK_ENABLED` controls migration fallback to
`AUTH_VIRTUAL_KEYS_JSON`. `AUTH_DEMO_KEYS_ENABLED` controls the two built-in demo
keys. Set both to `false` in production after persistent keys have been loaded.
The service fails closed on a PostgreSQL lookup error; `/readyz` verifies both
the connection and the migration.

## Internal virtual-key management

The gateway exposes the admin-RBAC API; auth owns the internal persistence
commands at `GET|POST /internal/v1/keys`, `PUT|DELETE /internal/v1/keys/{id}`,
and `POST /internal/v1/keys/{id}/{rotate|disable|enable}`. These endpoints require
`X-Management-Token: <MANAGEMENT_SHARED_SECRET>` plus request and actor audit
headers. They should remain cluster-internal and are not a replacement for
network policy. Create and rotate return the plaintext token once; PostgreSQL
stores only its HMAC-SHA256 lookup value. The list operation returns policy and
lifecycle metadata only. Its response
type has no plaintext-token or token-hash field.

Identity management uses `GET /internal/v1/{users|teams}`, `PUT
/internal/v1/users/{id}`, `PUT /internal/v1/teams/{id}`, and `PUT
/internal/v1/teams/{id}/members/{user_id}`. The public gateway applies global
admin or matching team-admin scope before calling these cluster-internal APIs.

## JWT and OIDC

Legacy mode supports `HS256` with `AUTH_JWT_SECRET`. For production OIDC, set a
direct `AUTH_JWT_JWKS_URL`; JWKS mode accepts only `RS256` and `ES256`, ignores
the HS256 secret, requires `iss`, `aud`, and `exp`, caches at most 64 signing
keys, and refreshes once when a previously unknown `kid` appears. Readiness
warms and validates the JWKS dependency. A JWKS fetch failure is reported as a
dependency failure rather than as an invalid client credential.

User, team, and role claims support dot-separated paths for nested OIDC claims.
The defaults are `sub`, `team_id`, and `roles`.

Environment:

```text
AUTH_JWT_SECRET=dev-jwt-secret
AUTH_JWT_ISSUER=ai-gateway
AUTH_JWT_AUDIENCE=ai-gateway
AUTH_JWT_JWKS_URL=
AUTH_JWT_JWKS_CACHE_TTL_SECONDS=300
AUTH_JWT_CLOCK_SKEW_SECONDS=30
AUTH_JWT_USER_ID_CLAIM=sub
AUTH_JWT_TEAM_ID_CLAIM=team_id
AUTH_JWT_ROLES_CLAIM=roles
AUTH_POSTGRES_KEYS_ENABLED=true
AUTH_POSTGRES_DSN=postgres://ai_gateway:password@postgres:5432/ai_gateway
AUTH_KEY_HASH_SECRET=separate-random-pepper
AUTH_STATIC_KEY_FALLBACK_ENABLED=false
AUTH_DEMO_KEYS_ENABLED=false
```
