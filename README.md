# AI Gateway

Модульный OpenAI-compatible gateway на Go: единая точка inference, управление
провайдерами и моделями, Virtual Keys, бюджеты, guardrails и наблюдаемость.

## Возможности

- Chat Completions, Responses с durable Conversations, Embeddings и Rerank API;
- native и synthetic SSE streaming;
- OpenAI/Azure/OpenAI-compatible, Anthropic, Ollama, Gemini, Cohere, Mistral и Voyage adapters;
- priority/weight/adaptive routing, retries, cooldown и cross-model fallback;
- независимые Providers, Credentials, Deployments, Model Groups и pricing;
- Virtual Keys, OIDC/JWT, organization/team/user scopes и Access Groups;
- budgets по identity, key, provider, model и tag;
- DLP, AV и request-scoped anonymization;
- exact/semantic cache, usage, request/audit logs, Prometheus и OTLP traces;
- встроенные React UI и Swagger UI без внешнего CDN.

## Документация

Начните с [индекса документации](docs/README.md). Документы разделены по
задачам:

- [быстрый запуск](docs/getting-started.md);
- [inference и совместимость](docs/inference.md);
- [managed control plane](docs/control-plane.md);
- [конфигурация](docs/configuration.md);
- [безопасность](docs/security.md);
- [MCP](docs/mcp.md);
- [архитектура](docs/architecture.md);
- [эксплуатация и проверки](docs/operations.md);
- [разработка и releases](docs/development.md).

Канонический HTTP-контракт находится в
[OpenAPI](repos/gateway/api/openapi.yaml). При включённом
`API_DOCS_ENABLED=true` gateway публикует `/docs/` и `/openapi.yaml`.

## Быстрый локальный запуск

Требуется Go из `repos/gateway/go.mod`:

```bash
cd repos/gateway
go run ./cmd/gateway
```

Без внешней конфигурации gateway использует demo provider и локальный auth:

```bash
curl -sS http://127.0.0.1:8080/v1/chat/completions \
  -H 'Authorization: Bearer demo-admin-key' \
  -H 'Content-Type: application/json' \
  -d '{"model":"demo-model","messages":[{"role":"user","content":"hello"}]}'
```

`docker-compose.yml` запускает только Redis, ClickHouse и PostgreSQL. Полный
стек для Rancher Desktop описан в руководстве по быстрому запуску.

## Компоненты

| Компонент | Порт | Назначение |
| --- | ---: | --- |
| Gateway | 8080 | Client/admin API, UI, routing и orchestration |
| Anonymizer | 8081 | Request-scoped masking |
| Auth | 8082 | Virtual Keys, JWT/OIDC и identity directory |
| Billing | 8083 | Budgets, usage, request logs и audit |
| DLP | 8084 | Text projection → ICAP |
| AV | 8085 | Text/images → ICAP |

Внутренние сервисы не являются клиентскими API. Bearer-токен получает только
Auth; остальные модули используют минимальные типизированные контракты и
отдельные service credentials.

## Структура репозитория

```text
repos/gateway/       gateway, OpenAPI и React UI
repos/auth/          authentication и identity
repos/billing/       budgets, usage и audit
repos/anonymizer/    masking service
repos/dlp/           DLP HTTP-to-ICAP adapter
repos/av/            AV HTTP-to-ICAP adapter
charts/              отдельные Helm charts
docs/                тематическая документация и LikeC4
migrations/          gateway-owned SQL migrations
```

Каждый `repos/*` — самостоятельный Go module. Команды тестирования и правила
синхронизации OpenAPI/миграций приведены в
[docs/operations.md](docs/operations.md).

## Лицензия

MIT.
