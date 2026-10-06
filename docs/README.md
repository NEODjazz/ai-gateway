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
- [Lemonade](lemonade.md) — подключение локального сервера, discovery моделей,
  поддерживаемые операции и ограничения адаптера.
- [Managed control plane](control-plane.md) — Providers/Credentials/Deployments,
  persistence, UI и audit.
- [Admin UI](admin-ui.md) — все 36 страниц, подменю, URLs, capabilities и основные настройки.
- [Browser SSO и API issuers](admin-sso-settings.md) — независимый trust,
  encrypted configuration, test/activate/rollback и recovery.
- [Organization identity](organization-identity.md) — tenant boundary, directory
  bindings, `org_admin` и изоляция ресурсов.
- [Keycloak/OpenWebUI](identity-keycloak-openwebui.md) — provisioning, OAuth,
  refresh и проверка реальными сервисами.
- [PDF / Docling](document-processing.md) — native или preprocessing,
  API/очередь/workers, quotas, ошибки и ограничения OCR.
- [Prompt injection protection](prompt-injection.md) — policies, heuristics,
  classifier deployment, errors, вложения и billing.
- [Конфигурация](configuration.md) — основные environment variables,
  providers, capabilities, модули и хранилища.
- [Индекс environment names](environment-reference.md) — production Go reads
  по всем шести сервисам со ссылками на код.
- [Безопасность](security.md) — authentication, service DTO, secrets и
  guardrails.
- [Эксплуатация](operations.md) — health/readiness, миграции, тесты,
  обновление и диагностика.
- [Разработка](development.md) — модули, правила изменения контрактов, CI и
  releases.

## Устройство системы

- [Архитектура](architecture.md) — границы сервисов, порядок запроса,
  routing/failover, безопасность контента и хранилища.
- [Resource lifecycle](resource-lifecycle.md) — ownership, background execution,
  billing reserve/commit/cancel и delivery recovery.
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
| Docling (optional) | [Guide](document-processing.md) | Internal PDF API, Redis RQ и CPU workers |

## API и разработка

- [Следующий этап](functional-roadmap.md) — требования к Keycloak/OpenWebUI,
  функциональный inventory, приоритеты, критерии приёмки и трудоёмкость.
- Каноническая спецификация: [OpenAPI](../repos/gateway/api/openapi.yaml).
- [Полный индекс HTTP API](api-index.md) — каждый method/path/operation ID из OpenAPI.
- [API coverage](api-coverage.md) — API families, provider-specific ограничения и подтверждения.
- [Lemonade validation](lemonade-validation.md) — opt-in реальные проверки API.
- [Playground functional plan](playground-functional-plan.md) — UI сценарии и критерии приёмки.
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

После изменения routes/OpenAPI или Go environment reads выполните
`python3 scripts/update-documentation.py`, затем
`python3 scripts/update-documentation.py --check`. Generated inventories не
заменяют объяснение defaults, policy или failure behavior; обновите профильный
guide одновременно. Проверка inventory выполняется в CI.

## Аудиты и история

- [Documentation audit](documentation-audit.md) — область проверки, исправленные
  расхождения и границы доказанного покрытия.
- [Production reliability](production-reliability.md) — накопленные implementation
  notes и результаты отдельных поставок; проверяйте scope и дату каждого пункта.
- [Reliability audit 2026-09-08](audits/2026-09-08-reliability-audit.md) — исторический
  снимок, который не определяет текущие capabilities.

Roadmaps фиксируют исходную оценку и результаты этапов. Runtime-настройки,
текущие URL и limits следует брать из тематических guides и исполняемых contracts.
