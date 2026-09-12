# Безопасность и границы данных

## Authentication

Клиент передаёт `Authorization: Bearer <credential>`. Gateway отправляет токен
только Auth contract `{token}`. Auth возвращает identity, opaque credential ID,
roles, tags и grants; после этого gateway очищает Bearer из `RequestContext`.

Virtual Keys хранятся как HMAC-SHA256 lookup values с отдельным
`AUTH_KEY_HASH_SECRET`. Plaintext показывается один раз после create/rotate.
Production OIDC использует direct JWKS URL, точные issuer/audience и только
RS256/ES256. HS256 и built-in demo/static keys предназначены для migration и
локальной разработки.

## Межсервисные контракты

| Сервис | Получает | Не получает |
| --- | --- | --- |
| Auth | Bearer token | Prompt/provider response |
| DLP | `request_id`, text projection | Bearer, identity, полный context |
| AV | `request_id`, text, validated image attachments | Bearer, identity |
| Anonymizer | Maskable text fields | Bearer, identity, image URL/base64 |
| Billing | Identity fingerprint, route metadata, counters/pricing | Prompt, response, raw provider error |

`RequestContext` не является network DTO. Remote responses применяются по
allowlist полей и не могут перезаписать identity или routing state целиком.

Internal `/authorize`, `/usage`, `/scan`, `/anonymize` и `/internal/v1/*`
должны оставаться cluster-internal. Service contracts защищаются отдельными
`MANAGEMENT_SHARED_SECRET`, `BILLING_SHARED_SECRET` и
`BILLING_MANAGEMENT_SHARED_SECRET`; ни один из них не должен совпадать с
Virtual Key или provider credential.

## Guardrails

Для каждой provider attempt порядок следующий: DLP, AV, anonymization, billing
reserve, provider, billing commit/cancel, deanonymization. DLP/AV получают
проекцию до masking, поэтому scanner видит исходный чувствительный текст, но не
identity/credential.

Policy attachment может ограничивать DLP/AV по team, opaque key ID/alias,
public model и tags. Dimensions соединяются AND, значения внутри dimension —
OR, поддержан только trailing `*`. Policies primary и всех допустимых fallback
targets объединяются консервативно. Missing/disabled required policy, content
rejection и AV failure для image request закрывают запрос.

Guardrail Monitor сохраняет только bounded metadata: request ID, policy,
module, source, outcome, duration и timestamp. Submitted text и raw scanner
response не сохраняются.

## MCP

Tool grants проверяются до provider pipeline. Для Chat Completions MCP обычно
исполняет клиент; gateway видит лишь function tools. В Responses connector
definition передаётся upstream provider, который выполняет transport. Gateway
не подставляет Virtual Key в connector headers. Для собственных discovery и
tool-call routes gateway может использовать отдельный server bearer credential:
он хранится в durable admin state только в AEAD ciphertext, не возвращается API
и отправляется без redirects только настроенному public HTTPS endpoint.

Полная модель разрешений и ограничения: [MCP](mcp.md).

## Secrets и deployment

- не храните provider keys, DSN passwords, encryption keys и shared secrets в
  Git/Helm plaintext values;
- используйте стабильный control-plane encryption key и резервное копирование
  вместе с ним;
- не отключайте TLS verification для provider/identity endpoints;
- публикуйте только Gateway Ingress; internal Services ограничивайте Network
  Policy;
- Swagger `Try it out` и demo/static auth отключайте вне trusted environment;
- Request Logs, traces и metrics не должны содержать prompt, response, Bearer,
  raw provider error или unbounded tenant labels.
