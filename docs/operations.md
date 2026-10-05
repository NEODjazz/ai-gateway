# Эксплуатация и проверка

## Health и readiness

| Сервис | Liveness | Readiness | Примечание |
| --- | --- | --- | --- |
| Gateway | `/healthz` | `/readyz` | Проверяет настроенные Redis и control-plane PostgreSQL |
| Auth | `/livez`, `/healthz` | `/readyz` | В durable/JWKS mode проверяет PostgreSQL migrations и JWKS |
| Billing | `/livez` | `/healthz` | `/healthz` одновременно проверяет Billing, audit storage и migrations |
| Anonymizer | `/healthz` | нет отдельного | ICAP не используется |
| DLP | `/healthz` | `/readyz` | Readiness выполняет content-free ICAP `OPTIONS` probe |
| AV | `/healthz` | `/readyz` | Readiness выполняет content-free ICAP `OPTIONS` probe |

Gateway `/metrics` экспортирует Prometheus text format. Dynamic request IDs,
tenant IDs и неизвестные paths не используются как неограниченные labels.

## Миграции

Helm charts PostgreSQL и ClickHouse запускают migration Jobs. Перед обновлением
приложений проверьте completion job и наличие всех файлов из:

- `repos/auth/migrations/postgres`;
- `repos/billing/migrations/postgres`;
- `repos/billing/migrations/clickhouse`;
- все Gateway migrations из `migrations/postgres`.

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

Для локального Rancher Desktop сначала подтвердите, что приложение запущено,
Docker socket доступен и Kubernetes включён. `rdctl list-settings` — read-only;
не меняйте runtime/settings ради rollout. Всегда выбирайте нужный context
явно; локальная сборка не означает, что image доступен другому container runtime.

```bash
docker context show
docker info --format '{{.ServerVersion}}'
kubectl --context rancher-desktop get nodes
helm --kube-context rancher-desktop list -n ai-gateway
```

Используйте один version-controlled environment values-файл без plaintext
secrets и передавайте секретные значения отдельным защищённым способом.

```bash
helm lint charts/ai-gateway
helm --kube-context rancher-desktop upgrade --install ai-gateway charts/ai-gateway -n ai-gateway -f values.local.yaml
kubectl --context rancher-desktop rollout status -n ai-gateway deployment/ai-gateway-gateway --timeout=120s
kubectl --context rancher-desktop get pods -n ai-gateway
```

При переходе со старого combined release сначала обновите `ai-gateway`, чтобы
он удалил ранее принадлежавшие ему module Deployments/Services, затем ставьте
отдельные charts. Helm не принимает ресурс, принадлежащий другому release.

Проверьте exact image tag/digest у нового pod, readiness, restart count, ingress,
UI HTML и JS/CSS, а также ожидаемый `401` у API без credential. Deployment Ready
не доказывает provider compatibility: отдельно выполните небольшой разрешённый
inference запрос. Не выводите environment values или полный Helm manifest с
Secret data в логи проверки. Docs-only изменения не требуют нового binary image.

## Резервное копирование и восстановление

| Данные | Что сохранять | Граница восстановления |
| --- | --- | --- |
| PostgreSQL | Auth directory/keys/SSO, Gateway control plane/files/jobs, Billing ledger/audit/outbox | Используйте согласованный backup и все migrations; restore в изолированную БД до переключения services |
| Encryption и hashing keys | `CREDENTIAL_ENCRYPTION_KEY`, совместимые legacy aliases, `AUTH_KEY_HASH_SECRET` | Backup отдельно через защищённый secret store; без прежних ключей ciphertext/lookup values не восстанавливаются |
| ClickHouse | Usage/reporting data и schema migrations | Durable billing outbox доставляет pending events, но не является вечным backup всех уже доставленных событий |
| Gateway Redis | Cache, rate/circuit state и legacy affinity при использовании | Потеря runtime state отличается от потери финансового ledger; не восстанавливайте устаревшие grants как источник truth |
| Docling Redis/PVC | Незавершённые PDF jobs/results, если нужна continuity | Содержит документные данные; AOF everysec не гарантирует zero-loss queue, TTL cleanup продолжает действовать |

Конкретный backup инструмент зависит от PostgreSQL/ClickHouse installation;
repository не предоставляет автоматический disaster-recovery controller.
Проверяйте restore на отдельном окружении и сопоставляйте schema versions,
control-plane revision, key decryption, tenant access, ledger/outbox и report
totals. Не включайте реальные credentials или пользовательские документы в
test fixtures. Rotation encryption key не равна переименованию env variable;
изменение значения требует отдельного плана re-encryption и проверки rollback.

После outage сначала восстановите durable stores и внутренние dependencies,
затем Auth/Billing и Gateway. Наблюдайте outbox backlog/delivery failures до
возврата отчётов в нормальное состояние. Не повторяйте inference, чтобы
«восстановить» недоставленные Usage events. Подробнее:
[resource lifecycle](resource-lifecycle.md), [SSO recovery](admin-sso-settings.md).

## Диагностика запроса

1. Получите `X-Execution-ID` ответа inference. Внешний `X-Request-ID` остается идентификатором корреляции; billing и Request Logs используют execution ID.
   HTTP-логи и trace attributes записывают для переданного клиентом `X-Request-ID`
   значение `sha256:` плюс hex SHA-256 от его trimmed значения; ответ клиенту
   возвращает исходный ID. Для сгенерированного gateway ID хэширование не применяется.
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
Generated API/configuration inventories: `python3 scripts/update-documentation.py --check`.
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
