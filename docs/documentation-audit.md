# Проверка документации — 2026-10-05

Проверена документация относительно кодовой базы `6c8b1235` и изменений этой
поставки. До проверки основные API и reliability scenarios уже были описаны,
но общие guides отставали от реализации SSO, навигации UI и Docling. Поэтому
ответ на вопрос «всё ли описано» был отрицательным: часть сведений была устаревшей,
а существующие подробные guides отсутствовали в общем индексе.

## Область проверки и источники

| Область | Исполняемый источник | Текущее руководство |
| --- | --- | --- |
| Public HTTP API | Gateway routes, native GenerateContent routes и OpenAPI | [Все 296 операций / 213 путей](api-index.md), [inference](inference.md), [API families](api-coverage.md) |
| Providers/models/deployments | `internal/provider`, capability validators, managed state | [Control plane](control-plane.md), [configuration](configuration.md), [Lemonade](lemonade.md) |
| Environment configuration | Production environment reads шести Go modules | [172 component/name entries](environment-reference.md), [defaults и ограничения](configuration.md), README компонентов |
| Admin UI | Route manifest, navigation manifest, session capabilities | [Все 36 страниц](admin-ui.md), [UI README](../repos/gateway/ui/README.md), [Playground](playground-functional-plan.md) |
| Identity и tenancy | Auth JWT/SSO/directory, Gateway RBAC | [SSO](admin-sso-settings.md), [organization](organization-identity.md), [Keycloak/OpenWebUI](identity-keycloak-openwebui.md), [security](security.md) |
| Guardrails и MCP | Module DTO, anonymizer rules, MCP runtime/ACL | [Security](security.md), [MCP](mcp.md), README DLP/AV/Anonymizer |
| Billing, cache и jobs | Billing reservations/outbox, provider ownership, controlstore | [Lifecycle](resource-lifecycle.md), [architecture](architecture.md), [reliability notes](production-reliability.md) |
| PDF conversion | Gateway preparer/client, Python API, Redis RQ, Helm stack | [Docling: flow, ограничения, ошибки](document-processing.md) |
| Runtime/CI/release | Dockerfiles, восемь charts, test/release workflows | [Operations](operations.md), [development](development.md), [getting started](getting-started.md) |
| Архитектурные схемы | Те же service/data boundaries | [Mermaid](architecture.md), [LikeC4](likec4/README.md), SSO/PDF/lifecycle схемы в guides |

Environment count включает одинаковое имя, прочитанное разными сервисами.
Индекс покрывает буквальные Go environment reads, а не все параметры внешних
контейнеров и динамически вычисляемые имена. API inventory включает management,
identity/SCIM, operational и native routes; статические UI/docs assets не являются
отдельными operation contracts.

## Исправленные расхождения

- Убрано описание browser SSO как источника JWT trust для всех API клиентов и
  environment fallback; описаны независимые connections/issuers и server sessions.
- Указаны действующие UI блоки, подменю, страницы, capabilities и пути к настройкам.
- Разъяснены Model/Deployment/adapter capabilities и отличие native `file_input`
  от document preprocessing.
- Добавлен Docling в общую архитектуру и LikeC4: API, dedicated Redis RQ и workers;
  описаны conversion pipeline, cancellation boundary и status/code diagnostics.
- Redis в LikeC4 больше не представлен исключительно будущим anonymizer vault.
  Добавлены текущие cache/limits/circuit, PostgreSQL control-plane/identity edges.
- Billing sequence показывает durable ledger/outbox и асинхронную доставку;
  `request_id` в ledger явно связан с execution ID. Content rejection исключён
  из схемы допускающего failover сценария.
- Поправлены README license, provider inventory, UI Search/Skills/MCP descriptions,
  ownership storage, migration inventory и описание обязательных CI integrations.
- Дополнены A2A/legacy SSO/AWS environment сведения, backup/restore boundaries и
  ссылки на уже существовавшие профильные guides.

## Проверка и дальнейшее сопровождение

`python3 scripts/update-documentation.py --check` проверяет generated HTTP/config
inventories, соответствие API routes, а также все UI page URLs/capabilities и
navigation membership. CI запускает эту проверку. Go API tests дополнительно
проверяют OpenAPI, относительные Markdown links и embedded migration copies.
LikeC4 проверяется закреплённым CLI из его README. Mermaid должен проходить
syntax/render check при изменении схем; `git diff --check` проверяет whitespace.

Инвентаризация доказывает наличие контракта, а не поддержку каждого endpoint
каждой моделью. Live provider tests и real database/OCR integration suites
имеют отдельные требования; documentation-only проверка их не заменяет.
Русский OCR, автоматический disaster recovery и незавершённые roadmap items
не объявляются реализованными. Historical audit/roadmap records оставлены с
их исходным scope; текущие operator decisions следует принимать по тематическим
guides и текущему исполняемому контракту.

В этой поставке выполнены: `gofmt -w .`, `go vet ./...`, `go test ./...` и
`go build ./...` в Gateway module; 10 Python tests для documentation inventory,
smoke routing и identity profile; inventory `--check`, Markdown file/heading
links и `git diff --check`. Все девять Mermaid схем отрендерены в Chrome,
LikeC4 CLI подтвердил модель и все пять views открыты в browser preview.
Снимки визуальной проверки не являются runtime fixtures.

Real PostgreSQL/ClickHouse/OCR/provider integration suites, UI bundle/container
build и race tests заново не запускались: эта поставка меняет документацию и
offline inventory tooling, а не runtime/concurrency или container definitions.
Новый CI step проверен локальными Python tests; полный remote CI имеет свой
отдельный статус после push.
