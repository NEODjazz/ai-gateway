# LikeC4-схемы

`architecture.likec4` содержит логическую модель, сценарии обработки запроса и Kubernetes deployment model для текущей архитектуры AI Gateway.

Доступные views:

- `index` — контекст системы;
- `services` — сервисы, провайдеры и хранилища;
- `request_flow` — последовательность успешного нестрируемого запроса;
- `failover` — provider attempt и переход к следующему endpoint;
- `kubernetes` — развертывание Helm-релизов в namespace `ai-gateway`.

Проверка и локальный просмотр:

```powershell
npx likec4 validate docs/likec4
npx likec4 start docs/likec4
```

Пунктирная связь `planned` показывает еще не реализованную интеграцию anonymizer → Redis. Billing уже использует PostgreSQL для atomic budgets и durable outbox.
