# Managed control plane

## Ресурсы и зависимости

Конфигурация намеренно разделена на независимые сущности:

```text
Provider ──┐
           ├─> Deployment ─> Model Group
Credential ┘       │
                   └─> Model Catalog pricing/capabilities
```

- Provider хранит adapter type, base URL и enabled state.
- Credential хранит write-only secret, optional provider binding и metadata.
- Deployment связывает provider/credential с upstream и public models,
  capabilities, routing, admission и guardrail policy.
- Model Group задаёт public alias, deployment membership, strategy, retries и
  fallbacks.
- Model Catalog хранит versioned pricing, limits и model capabilities.

UI выбирает существующие IDs из списков. API намеренно принимает ссылки по ID,
проверяет provider binding и возвращает `409 resource_in_use`, если удаление
сломает Deployment, Model Group или fallback chain.

## Persistence

Без `PROVIDER_CONTROL_PLANE_POSTGRES_DSN` управление process-local; encryption
key генерируется на процесс и подходит только для разработки. С DSN gateway
требует стабильный `PROVIDER_CREDENTIAL_ENCRYPTION_KEY` минимум из 16 символов.

Providers, encrypted Credentials, Deployments, Model Groups, model catalog,
guardrails/attachments, tags, projects/access groups, MCP, agent/tool policies и
logging destinations сохраняются в одном versioned JSONB snapshot. Запись
использует optimistic revision; конфликт другой replica возвращает
`409 revision_conflict`, а неуспешная persistence откатывает runtime mutation.

Credentials и logging secrets сохраняются AES-GCM ciphertext+nonce. Read API
возвращает только metadata; пустой secret при edit сохраняет прежнее значение,
а rotate является отдельной audited операцией.

## UI

Встроенный route-based React UI доступен на `/ui/` и использует только
документированные `/admin/v1/*` endpoints. Он не является доверенной security
boundary: gateway повторно выполняет authentication, RBAC и audit.

Основные блоки:

- Manage: Virtual Keys, Providers, Credentials, Deployments, Models и Model
  Groups;
- Access Control: Organizations, Teams, Users, Access Groups и Projects;
- Monitor: Overview, Usage & Spend, Customer Insights, Logs, Routing и
  Playground;
- AI Hub/Govern/System: MCP, agent/tool policies, guardrails, cache, budgets и
  logging destinations.

Route manifest содержит 36 навигационных страниц. Таблицы используют общий
поиск, columns/filter menus, sorting, pagination и action menu. Детали UI и
frontend checks описаны в
[UI README](../repos/gateway/ui/README.md).

## Audit и observability

Management mutation сначала пишет PostgreSQL audit event `attempted`, затем
`succeeded` или `failed`. Side effect не выполняется, если audit preflight
недоступен. Audit identity берётся из authenticated gateway context, а не из
клиентского JSON.

Usage, request logs и audit logs остаются разными источниками. UI не вычисляет
ledger самостоятельно и не объединяет расходы разных currencies.
