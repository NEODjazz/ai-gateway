# AI Gateway - Anonymizer Service

Anonymizer microservice for masking sensitive data before AI provider calls and preserving request-local placeholder mappings.

## Run

```powershell
go run ./cmd/anonymizer
```

## Endpoints

- `GET /healthz`
- `POST /anonymize`

The endpoint accepts only maskable content (`messages`, Responses API `input`
and `instructions`, or rerank `query` and `documents`) plus `request_id`. It does not receive bearer credentials or
user identity.

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
and template macros are not supported.
