# Эксплуатация и проверка

## Health и readiness

| Сервис | Liveness | Readiness | Примечание |
| --- | --- | --- | --- |
| Gateway | `/healthz` | `/readyz` | Проверяет настроенные Redis и control-plane PostgreSQL |
| Auth | `/livez`, `/healthz` | `/readyz` | В durable/JWKS mode проверяет PostgreSQL migrations и JWKS |
| Billing | `/livez` | `/healthz` | `/healthz` одновременно проверяет Billing, audit storage и migrations |
| Anonymizer | `/healthz` | нет отдельного | ICAP не используется |
| DLP | `/healthz` | нет отдельного | Health не выполняет ICAP probe |
| AV | `/healthz` | нет отдельного | Health не выполняет ICAP probe |

Gateway `/metrics` экспортирует Prometheus text format. Dynamic request IDs,
tenant IDs и неизвестные paths не используются как неограниченные labels.

## Миграции

Helm charts PostgreSQL и ClickHouse запускают migration Jobs. Перед обновлением
приложений проверьте completion job и наличие всех файлов из:

- `repos/auth/migrations/postgres`;
- `repos/billing/migrations/postgres`;
- `repos/billing/migrations/clickhouse`;
- `migrations/postgres/007_gateway_control_plane.sql`.

Charts embed copies of these SQL files in their migration ConfigMaps. При
изменении SQL обновите и source-файл, и соответствующий template в
`charts/postgres` или `charts/clickhouse`; иначе локальные integration tests и
Helm deployment будут использовать разные схемы.

Номера файлов могут иметь намеренные пропуски: порядок определяется именем, а
не требованием непрерывной нумерации. Billing binary с новыми usage columns
нельзя запускать до соответствующей ClickHouse migration.

```bash
kubectl get jobs,pods -n ai-gateway
kubectl logs -n ai-gateway job/<migration-job>
```

Migration Jobs являются Helm hooks с `hook-succeeded` cleanup, поэтому успешно
завершённый Job может уже отсутствовать. Failed/running hook остаётся доступен
для `kubectl logs`; результат upgrade также проверяйте через `helm status`.

Budget deletion является soft disable. Virtual Key deletion означает revoke.
MCP server/toolset и Access Group имеют referential checks; сначала удалите
назначения, если API возвращает `409 resource_in_use`.

## Обновление Helm

Используйте один version-controlled environment values-файл без plaintext
secrets и передавайте секретные значения отдельным защищённым способом.

```bash
helm lint charts/ai-gateway
helm upgrade --install ai-gateway charts/ai-gateway -n ai-gateway -f values.local.yaml
kubectl rollout status -n ai-gateway deployment/ai-gateway-gateway
kubectl get pods -n ai-gateway
```

При переходе со старого combined release сначала обновите `ai-gateway`, чтобы
он удалил ранее принадлежавшие ему module Deployments/Services, затем ставьте
отдельные charts. Helm не принимает ресурс, принадлежащий другому release.

## Диагностика запроса

1. Получите `X-Execution-ID` ответа inference. Внешний `X-Request-ID` остается идентификатором корреляции; billing и Request Logs используют execution ID.
2. Найдите metadata-only запись на Logs → Request Logs или через
   `/admin/v1/request-logs?request_id=...`.
3. Проверьте public/upstream model, provider endpoint, status/failure class,
   retries/fallbacks, cache state и usage source.
4. Откройте Routing diagnostics для circuit, admission и candidate ordering.
5. Для streaming используйте `curl -N`; gateway пишет `X-Accel-Buffering: no`
   и flush-ит SSE. Крупные upstream chunks остаются крупными.

Request Logs не содержат prompt, response или raw provider error. MCP
`tools/call`, выполненный клиентом вне gateway, не является gateway event.
Вызов через публичный gateway MCP route фиксируется как `mcp_tools_call` и
увеличивает `tool_requests` после успешного protocol result.

## Проверки кода

Каждый сервис — отдельный Go module, поэтому команды запускаются в его каталоге:

```bash
cd repos/gateway
gofmt -w .
go vet ./...
go test ./...
go test -race ./...
go build ./...
```

Повторите для `repos/auth`, `repos/billing`, `repos/anonymizer`, `repos/dlp` и
`repos/av`. Integration tests PostgreSQL/Redis пропускаются без явно заданных
test DSN/addresses; наличие skip в выводе важно проверить отдельно.

Frontend:

```bash
cd repos/gateway/ui
npm ci
npm run typecheck
npm test
npm run test:coverage
npm run build
```

OpenAPI route coverage и schema validation находятся в `repos/gateway/api`.
LikeC4 проверяется инструкциями из `docs/likec4/README.md`. После изменения
документации выполните `git diff --check` и проверьте относительные Markdown
links.

## Контейнеры

При изменении Dockerfile соберите соответствующий image Rancher Desktop Docker
CLI. Gateway image также собирает UI:

```bash
docker build -t ai-gateway-gateway:local repos/gateway
docker image inspect ai-gateway-gateway:local
```

Runtime images запускаются non-root пользователем. Не добавляйте credentials,
`.env` или исходные secret values в build context/layers.
