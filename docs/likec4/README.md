# LikeC4-схемы

`architecture.likec4` содержит логическую модель, сценарии обработки запроса и Kubernetes deployment model для текущей архитектуры AI Gateway.

Доступные views:

- `index` — контекст системы;
- `services` — сервисы, провайдеры и хранилища;
- `request_flow` — последовательность успешного нестрируемого запроса;
- `failover` — provider attempt и переход к следующему endpoint;
- `kubernetes` — развертывание Helm-релизов в namespace `ai-gateway`.

Проверка и локальный просмотр:

```bash
npx --yes likec4@1.59.2 validate docs/likec4
npx --yes likec4@1.59.2 start docs/likec4
```

Пунктирная связь `planned` показывает еще не реализованную интеграцию anonymizer → Redis. Billing уже использует PostgreSQL для atomic budgets и durable outbox.

Optional Docling API, dedicated Redis RQ и CPU workers входят в `services` и
`kubernetes`; они включаются в gateway Helm release только при `docling.enabled`.
Gateway Redis уже используется для limits/cache/circuit state; planned vault
связь относится только к anonymizer. Control plane и Auth identity/session
state сохраняются в PostgreSQL отдельно от финансового ledger.
