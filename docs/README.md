# Документация AI Gateway

Документы разделены по пользовательским задачам. Если текст расходится с
OpenAPI или исполняемым кодом, источником истины для HTTP-контракта является
`repos/gateway/api/openapi.yaml`, а для runtime-поведения — соответствующий Go
package и Helm template.

## Начало работы

- [Быстрый запуск](getting-started.md) — локальный demo, Rancher Desktop,
  проверка API и UI.
- [Inference API](inference.md) — endpoints, capabilities, adapters, routing,
  streaming и cache.
- [Managed control plane](control-plane.md) — Providers/Credentials/Deployments,
  persistence, UI и audit.
- [Конфигурация](configuration.md) — основные environment variables,
  providers, capabilities, модули и хранилища.
- [Безопасность](security.md) — authentication, service DTO, secrets и
  guardrails.
- [Эксплуатация](operations.md) — health/readiness, миграции, тесты,
  обновление и диагностика.
- [Разработка](development.md) — модули, правила изменения контрактов, CI и
  releases.

## Устройство системы

- [Архитектура](architecture.md) — границы сервисов, порядок запроса,
  routing/failover, безопасность контента и хранилища.
- [MCP](mcp.md) — клиентское исполнение OpenCode, Responses passthrough,
  Toolsets и границы ACL.
- [Модельный каталог](model-catalog.example.json) — пример versioned pricing и
  capabilities catalog.
- [LikeC4](likec4/README.md) — редактирование и проверка архитектурных схем.

## Компоненты

| Компонент | Документ | Публичная роль |
| --- | --- | --- |
| Gateway | [README](../repos/gateway/README.md) | Inference API, admin API, routing и UI |
| Admin UI | [README](../repos/gateway/ui/README.md) | Route-based control plane и Playground |
| Auth | [README](../repos/auth/README.md) | Virtual Keys, JWT/OIDC и identity directory |
| Billing | [README](../repos/billing/README.md) | Budgets, usage, request logs и audit |
| Anonymizer | [README](../repos/anonymizer/README.md) | Request-scoped masking |
| DLP | [README](../repos/dlp/README.md) | Text projection → ICAP |
| AV | [README](../repos/av/README.md) | Text/images → ICAP |

## API и разработка

- Каноническая спецификация: [OpenAPI](../repos/gateway/api/openapi.yaml).
- При `API_DOCS_ENABLED=true`: `/docs/` и `/openapi.yaml` на gateway.
- Все `/admin/v1/*` операции проходят gateway authentication и RBAC.
- `/internal/v1/*`, `/authorize`, `/usage`, `/scan` и `/anonymize` —
  межсервисные контракты. Их нельзя публиковать как клиентский API.
- Команды проверки находятся в [эксплуатационном руководстве](operations.md).

## Как поддерживать документацию

Изменение публичного gateway API требует одновременно обновить route contract,
OpenAPI schema/example и тесты `repos/gateway/api`. Изменение environment
variable требует обновить `configuration.md`, README сервиса и Helm
values/template. Изменение границы данных между сервисами требует обновить
`architecture.md` и contract tests удалённого модуля.
