# AI Gateway - Anonymizer Service

Anonymizer microservice for masking sensitive data before AI provider calls and preserving request-local placeholder mappings.

## Run

```powershell
go run ./cmd/anonymizer
```

## Endpoints

- `GET /healthz`
- `GET /rules`
- `POST /anonymize`

The endpoint accepts only maskable content (`messages`, Responses API `input`
and `instructions`, or rerank `query` and `documents`) plus `request_id`. It does not receive bearer credentials or
user identity.

`POST /anonymize` may include a request-scoped `rules` array. When omitted, the
service uses every rule enabled by `ANONYMIZER_RULES`. Unknown names fail the
request instead of silently weakening masking. `GET /rules` exposes the active
rule names for the gateway management UI; it does not expose patterns or
replacement values.

## Rules

Configure with `ANONYMIZER_RULES`, for example:

```text
ANONYMIZER_RULES=email,phone,ip
```

Rule definitions can be loaded from a JSON file through
`ANONYMIZER_RULES_CONFIG_PATH`. The Helm chart creates and mounts this file from
`anonymizer.ruleDefinitions`. Each definition contains `name`, `placeholder`,
and a Go-compatible regular expression in `pattern`. The optional `validator`
currently supports `luhn` for bank card candidates.

Contextual rules may also set `capture_group` to a positive group number. In
that case the anonymizer preserves the surrounding match and masks only that
captured value. Patterns must use Go/RE2 syntax; lookbehind, backreferences,
and template macros are not supported. `exclude_values` can list normalized,
case-insensitive capture values that the rule must preserve. Use explicit
Unicode boundaries such as `(?:^|[^\p{L}\p{N}_])` for Cyrillic labels because
RE2 `\b` uses ASCII word characters.
