# Admin UI: страницы и доступ

UI доступен на `/ui/`. Структура sidebar находится в
[navigation.ts](../repos/gateway/ui/src/app/navigation.ts), route guards — в
[routes.tsx](../repos/gateway/ui/src/app/routes.tsx). Ни скрытая ссылка, ни
сохранённое состояние меню не заменяют backend RBAC.

В таблице приведены 36 навигационных страниц. URL указан относительно `/ui`.
Capability приходит из проверенной `/admin/v1/session`; у routes без явной
capability требуется `admin`. `admin` также открывает organization reports/keys.
`org_admin` получает scoped возможности своей проверенной организации; роль не
даёт глобального управления. Детальные identity правила:
[Organization identity](organization-identity.md).

| Блок / подменю | Страница | URL | Capability |
| --- | --- | --- | --- |
| AI Gateway | Virtual keys | `/api-keys` | `organization_keys` |
| AI Gateway | Playground | `/playground` | `inference` |
| Models & endpoints | Models | `/models` | `admin` |
| Models & endpoints | Model onboarding | `/model-onboarding` | `admin` |
| Models & endpoints | Providers | `/providers` | `admin` |
| Models & endpoints | Credentials | `/credentials` | `admin` |
| Models & endpoints | Deployments | `/deployments` | `admin` |
| Models & endpoints | Model groups | `/model-groups` | `admin` |
| Agentic | Agent profiles | `/agents` | `admin` |
| MCP | MCP servers | `/mcp-servers` | `admin` |
| MCP | MCP toolsets | `/mcp-toolsets` | `admin` |
| AI Gateway | Skills | `/skills` | `inference` |
| Tools | Search | `/search-tools` | `inference` |
| Tools | Tool policies | `/tool-policies` | `admin` |
| AI Gateway | Guardrails | `/guardrails` | `admin` |
| AI Gateway | Policies | `/policies` | `admin` |
| Observability | Overview | `/overview` | `admin` |
| Observability | Usage & spend | `/usage` | `organization_reports` |
| Observability | Customer insights | `/customers` | `admin` |
| Observability | Cost optimization | `/cost-optimization` | `admin` |
| Observability | Logs | `/logs` | `organization_reports` |
| Observability | Routing diagnostics | `/routing` | `admin` |
| Observability | Guardrail monitor | `/guardrails-monitor` | `admin` |
| Access Control | Organizations | `/organizations` | `admin` |
| Access Control | Teams | `/teams` | `team_directory` |
| Access Control | Users | `/users` | `team_directory` |
| Access Control | Access groups | `/access-groups` | `admin` |
| Access Control | Projects | `/projects` | `admin` |
| Access Control | Budgets | `/budgets` | `admin` |
| Developer Tools | API reference | `/api-reference` | `api_docs` |
| Developer Tools | AI Hub | `/ai-hub` | `admin` |
| Developer Tools | Response cache | `/cache` | `admin` |
| Developer Tools | Tag management | `/tag-management` | `admin` |
| Settings / Settings | Router settings | `/router-settings` | `admin` |
| Settings / Settings | Logging & alerts | `/logging` | `admin` |
| Settings / Settings | Single sign-on | `/settings` | `admin` |

Resource detail URLs не являются дополнительными sidebar entries и могут иметь
отдельные guards. Например, scoped key list не означает глобальный доступ к
`/api-keys/:id`. Недоступные пункты, пустые подменю и блоки скрываются. Подменю
активной страницы автоматически раскрывается; preferences хранят только boolean
disclosure state. Клавиатура, compact mode и browser storage описаны в
[UI README](../repos/gateway/ui/README.md).

## Основные сценарии

Provider authentication настраивается через **Models & endpoints → Credentials**,
затем credential выбирается у deployment. Azure service principal, bearer/API key
и другие типы не являются browser SSO. Список полей определяется authentication
type; write-only secrets не возвращаются в Read API. См.
[конфигурацию](configuration.md) и [control plane](control-plane.md).

PDF: **Models & endpoints → Deployments → Edit → Document processing (PDF)**.
Выберите `native` либо `docling`; запуск optional stack и ограничения описаны в
[Document processing](document-processing.md).

Prompt injection: **AI Gateway → Guardrails → Create/Edit policy → Prompt injection
detection**. Доступны локальные heuristics и classifier deployment, настройки
fail-on-error, вложений и лимитов. Затем привяжите policy к deployment или через
Policies. **Test Guardrails** выполняет настоящую проверку; LLM-классификация
имеет отдельный billing. Детали и ограничения: [Prompt injection](prompt-injection.md).

Browser login: **Settings → Settings → Single sign-on**. Presets, connections,
проверка identity, activation/rollback и отдельные API issuers описаны в
[SSO settings](admin-sso-settings.md). Browser identity не изменяет API trust.

Usage/Logs читают серверные totals, валюты и проверенный organization scope.
Размер страницы не является общим количеством; недоступный источник не должен
отображаться как нулевой расход. CSV содержит выбранные данные и корректно
экранирует значения; экспорт не добавляет права доступа. Запоздавшие запросы не
должны заменять данные выбранного периода. Overview показывает доступные блоки
при отказе отдельного endpoint.

Playground использует реальный API и adapter capabilities. Test key хранится в
памяти компонента; для другого origin нужны explicit key и доверенный origin.
Session management, streaming, cancellation и attachment modes описаны в
[UI README](../repos/gateway/ui/README.md) и
[Playground plan](playground-functional-plan.md). Functional plans содержат также
приоритеты и ограничения, поэтому pending пункт не является обещанием текущей
реализации.
