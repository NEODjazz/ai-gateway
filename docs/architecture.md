# Архитектура AI Gateway

Документ описывает текущее состояние реализации в `repos/*` и развертывания в `charts/*`. Целевая функциональность, для которой инфраструктура уже подготовлена, но интеграция еще не реализована, помечена явно.

Исходники тех же схем в формате LikeC4 находятся в [`docs/likec4`](likec4/README.md).

## Обзор системы

AI Gateway — набор Go-сервисов с единой OpenAI-compatible точкой входа. Gateway аутентифицирует запрос, выбирает совместимые provider endpoints, выполняет отдельный provider-level pipeline для каждой попытки и возвращает обычный JSON или SSE stream.

```mermaid
flowchart LR
    Client["Client / OpenAI SDK"]

    subgraph Platform["AI Gateway platform"]
        Gateway["Gateway :8080<br/>API, routing, failover"]
        Auth["Auth :8082<br/>API keys / JWT / roles"]
        DLP["DLP :8084<br/>HTTP-to-ICAP adapter"]
        AV["AV :8085<br/>HTTP-to-ICAP adapter"]
        Anonymizer["Anonymizer :8081<br/>request-local masking"]
        Billing["Billing :8083<br/>usage and cost events"]
    end

    subgraph Providers["AI providers"]
        OpenAI["OpenAI-compatible / Azure / OpenRouter"]
        Anthropic["Anthropic"]
        Ollama["Ollama"]
        Demo["Demo fallback"]
    end

    DlpIcap["DLP ICAP server"]
    AvIcap["Antivirus ICAP server"]
    ClickHouse[("ClickHouse<br/>usage_events")]
    Redis[("Redis<br/>gateway limits + exact cache")]
    Postgres[("PostgreSQL<br/>billing ledger + durable outbox")]

    Client -->|"OpenAI-compatible HTTP/SSE"| Gateway
    Gateway -->|"POST /authorize"| Auth
    Gateway -->|"POST /scan, per enabled endpoint"| DLP
    Gateway -->|"POST /scan, per enabled endpoint"| AV
    Gateway -->|"POST /anonymize"| Anonymizer
    Gateway -->|"POST /usage, before and after provider"| Billing
    Gateway --> OpenAI
    Gateway --> Anthropic
    Gateway --> Ollama
    Gateway --> Demo
    DLP -->|"ICAP REQMOD"| DlpIcap
    AV -->|"ICAP REQMOD"| AvIcap
    Billing -->|"JSONEachRow over HTTP"| ClickHouse
    Gateway -->|"atomic limits + cache"| Redis
    Billing -->|"ledger + outbox"| Postgres
```

## Сервисы и ответственность

| Сервис | HTTP API | Текущая ответственность |
| --- | --- | --- |
| Gateway | `GET /healthz`, `GET /v1/models`, `POST /v1/chat/completions`, `POST /v1/responses` | OpenAI-compatible API, auth pipeline, provider routing, failover, SSE, orchestration provider-level modules, deanonymization |
| Auth | `GET /healthz`, `GET /readyz`, `POST /authorize` | PostgreSQL virtual keys с expiry/revoke/rotation, переходный static fallback и HS256 JWT; заполняет identity и access policy |
| DLP | `GET /healthz`, `POST /scan` | Извлекает текст запроса и отправляет его в настроенный ICAP-сервис через `REQMOD` |
| AV | `GET /healthz`, `POST /scan` | Аналогичный HTTP-to-ICAP адаптер для антивирусной проверки |
| Anonymizer | `GET /healthz`, `POST /anonymize` | Маскирует значения по настраиваемым RE2-правилам и возвращает преобразованный контент с placeholder map |
| Billing | `GET /healthz`, `POST /usage` | Оценивает/собирает tokens и cost, создает billing event, после ответа пишет usage event в ClickHouse |

`RequestContext` существует только внутри gateway. Между сервисами используются отдельные минимальные DTO: исходный bearer token получает только auth, DLP/AV получают текстовую проекцию, anonymizer — маскируемые поля, а billing — identity fingerprint, provider metadata и счетчики tokens без prompt/response content. Ответ каждого сервиса применяется к локальному контексту по явному allowlist полей.

## Обработка запроса

### Нестрируемый запрос

```mermaid
sequenceDiagram
    participant C as Client
    participant G as Gateway
    participant A as Auth
    participant D as DLP
    participant V as AV
    participant N as Anonymizer
    participant B as Billing
    participant P as AI provider endpoint
    participant CH as ClickHouse

    C->>G: /v1/chat/completions or /v1/responses
    G->>A: POST /authorize (token only)
    A-->>G: identity + roles
    loop each compatible endpoint by priority
        opt dlp_enabled
            G->>D: POST /scan (text projection)
            D-->>G: accepted context or 451/error
        end
        opt av_enabled
            G->>V: POST /scan (text projection)
            V-->>G: accepted context or 451/error
        end
        G->>N: POST /anonymize (maskable content only)
        N-->>G: masked request + placeholder map
        G->>B: POST /usage (identity fingerprint + counters)
        B-->>G: prompt estimate / policy result
        G->>P: provider-specific request
        alt provider succeeded
            P-->>G: response + optional usage
            G->>B: POST /usage (usage counters, no content)
            B->>CH: insert usage event
            B-->>G: enriched context
            G->>G: deanonymize response
            G-->>C: OpenAI-compatible response
        else modules/provider/post-billing failed
            G->>G: try next compatible endpoint
        end
    end
```

Порядок provider pipeline задан в `repos/gateway/cmd/gateway/main.go`:

1. `dlp` — только если у endpoint установлен `dlp_enabled`.
2. `av` — только если у endpoint установлен `av_enabled`.
3. `anonymizer` — для каждой попытки создается новая placeholder map.
4. `billing` pre-response — атомарно резервирует estimated input + maximum output allowance в PostgreSQL.
5. Вызов выбранного provider endpoint.
6. `billing` post-response — заменяет reservation фактическим provider usage; при окончательной ошибке выполняет cancel.
7. Gateway восстанавливает placeholders в успешном ответе.

Неуспешная попытка обязательного модуля или provider добавляется в агрегированную ошибку router, после чего router может перейти к следующему совместимому endpoint. Content rejection и budget rejection являются terminal: provider не вызывается, fallback не выполняется, клиент получает соответственно `451` или `429 budget_exceeded`. Если остальные кандидаты закончились, клиент получает `502 provider_failed`.

### Required и optional

Каждый модуль реализует базовый контракт:

```go
type Module interface {
    Name() string
    Required() bool
    Handle(ctx context.Context, req *RequestContext) error
}
```

Billing дополнительно реализует `PostResponseModule`. Ошибка `required`-модуля завершает текущую provider attempt; ошибка optional-модуля логируется, и pipeline продолжается. `ErrContentRejected` всегда завершает текущую попытку независимо от `required`.

Auth находится в gateway-level pipeline и выполняется один раз до маршрутизации. Остальные модули находятся в provider-level pipeline и получают независимый контекст на каждую попытку failover.

### Remote и in-process

- `auth`, `anonymizer` и `billing` используют удаленный HTTP-сервис, если задан соответствующий `*_URL`; без URL gateway создает локальную реализацию.
- `dlp` и `av` реализованы только как удаленные адаптеры. Они пропускаются, когда выключены для endpoint; включенный endpoint без URL дает ошибку модуля.
- HTTP timeout удаленного модуля в gateway — 2 секунды. Timeout ICAP-клиента DLP/AV настраивается отдельно и по умолчанию равен 5 секундам.

### Межсервисные границы данных

- `auth`: получает `{token}`, ищет persistent key по HMAC-SHA256 и возвращает `user_id`, `team_id`, policy и непрозрачный `credential_id`; после auth gateway очищает bearer из request context.
- `dlp` / `av`: получают только `request_id` и текстовую проекцию запроса.
- `anonymizer`: получает только messages/input/instructions и возвращает преобразованные поля с placeholder map.
- `billing`: получает identity, необратимый `credential_id`, provider/model metadata и token counters; prompt и provider response не передаются.

Admin management endpoints сначала проходят обычный auth pipeline и RBAC в
gateway. Клиентский bearer после этого очищается. Во внутренний auth management
endpoint передаются только key policy, `request_id`, actor ID, необратимый actor
credential ID и отдельный scoped service secret. Новый plaintext virtual key
возвращается только в ответе create/rotate и в базе не хранится.

HTTP-ответы модулей декодируются в типизированные DTO, поэтому удаленный сервис не может перезаписать identity, routing metadata или исходный запрос целиком.

## Маршрутизация и failover

Endpoints загружаются из `PROVIDERS_JSON`, выключенные endpoints отбрасываются, неизвестные типы игнорируются, остальные стабильно сортируются по возрастанию `priority`.

`MODEL_CATALOG_JSON` — версионированный общий контракт gateway и billing.
Gateway сопоставляет entry по endpoint name, затем provider type и `*`, проверяет
request-derived capabilities (`chat`, `responses`, `embeddings`, `stream`, `tools`,
`structured_output`) и `max_output_tokens`. При
`unknown_model_policy=deny` неизвестная модель не участвует в routing и не
публикуется через `/v1/models`. Billing по тому же precedence выбирает цену за
миллион input/output tokens и currency.

В режиме `ROUTING_STRATEGY=adaptive` порядок endpoints одного priority
уточняется по EWMA latency и failures; priority остаётся жёсткой границей.
Responses `previous_response_id` закрепляется за создавшим его endpoint в
tenant-scoped affinity store. С Redis это общий state для всех replicas, без
Redis — локальный memory fallback. Provider-scoped response ID не отправляется
другому endpoint при failover.

Semantic cache выключен по умолчанию. Для допустимого text-only chat запроса он
вычисляет embedding только после DLP/AV и anonymization. Scope включает
необратимый credential ID, authenticated user, endpoint, logical model и generation settings, поэтому
team membership само по себе не разделяет cache entries. Допускается ровно одно
user message и exact-matched system/developer context. Tools, tool results,
assistant history, structured output и multimodal content всегда обходят semantic cache. Embedder
использует отдельный service credential; клиентский bearer ему не передаётся.

```mermaid
flowchart TD
    Request["provider + model"] --> Mode{"Как задан запрос?"}
    Mode -->|"provider = endpoint name or type"| ByProvider["Filter by provider and model"]
    Mode -->|"provider = auto"| ByModel["Filter by model"]
    Mode -->|"provider empty, model set"| ByModel
    Mode -->|"provider and model empty"| Default["Filter by DEFAULT_PROVIDER"]
    ByProvider --> Sorted["Candidates already sorted by priority"]
    ByModel --> Sorted
    Default --> Sorted
    Sorted --> Attempt["New provider-attempt context and module pipeline"]
    Attempt --> Success{"Attempt succeeded?"}
    Success -->|yes| Return["Return response"]
    Success -->|no| More{"More candidates?"}
    More -->|yes| Attempt
    More -->|no| Error["502 provider_failed with joined errors"]
```

Поддерживаемые provider clients:

- `ollama`;
- `openai`, `openai-compatible`, `openrouter`;
- `anthropic`;
- `demo`.

Если после конфигурации не осталось ни одного endpoint, gateway создает `demo`. `GET /v1/models` возвращает уникальные модели из настроенных endpoints и также защищен auth pipeline.

### Streaming

Для endpoints с `stream: true` gateway пытается проксировать provider SSE. Если client не поддерживает streaming, gateway может выполнить обычный запрос и синтезировать SSE-ответ. После начала настоящего provider stream ошибка уже возвращается в этот stream, без failover на следующий endpoint.

Текущее ограничение: запрос перед streaming-вызовом анонимизируется, но provider chunks передаются клиенту напрямую; request-local deanonymization применяется только к собранному финальному response object и не преобразует уже отправленные chunks.

## Безопасность контента

DLP и AV получают текстовую проекцию запроса и вызывают внешний ICAP endpoint методом `REQMOD`. Ответ считается блокирующим при специальных infection/blocking headers либо при наличии `res-hdr` в `Encapsulated`. Адаптер преобразует блокировку в HTTP `451`, а gateway — в `ErrContentRejected`.

Включение проверок задается на каждом provider endpoint через `dlp_enabled` и `av_enabled`. Это означает, что failover endpoint должен иметь эквивалентную security policy, если обход проверки недопустим.

## Данные и хранилища

### Anonymization state

Placeholder map сейчас живет только в копии `RequestContext` конкретной provider attempt. Она очищается перед каждой следующей попыткой и используется gateway для восстановления успешного нестрируемого ответа.

Helm chart передает anonymizer переменную `REDIS_ADDR` и отдельно разворачивает Redis, однако текущий Go-код anonymizer не подключается к Redis. Redis-backed vault является подготовленной, но не реализованной частью архитектуры.

### Billing state

- ClickHouse используется как append-only хранилище `usage_events` через HTTP insert в `JSONEachRow`, когда `BILLING_USAGE_EVENTS_ENABLED=true`.
- Billing использует lifecycle `reserve -> commit/cancel`. При `BILLING_DURABLE_OUTBOX_ENABLED=true` ledger и outbox транзакционно сохраняются в PostgreSQL. Worker использует `SKIP LOCKED`, stale-lock recovery и backoff; `event_id=request_id:phase` дедуплицирует enqueue между репликами и рестартами. Доставка в ClickHouse имеет семантику at-least-once, поэтому точный финансовый расчет должен дедуплицировать события по `event_id`.
- PostgreSQL policy checker сериализует matching policies через row locks и атомарно применяет cost/token budgets по global/key/user/team/model/provider scope и hour/day/week/month period. Reserve учитывает максимальный output или безопасный fallback, commit — фактический usage, cancel и TTL освобождают capacity.
- Повторный active reserve с тем же `request_id` идемпотентен и при failover переносит reservation на новый provider/model scope. Повторное использование finalized `request_id` или смена billing identity отклоняется как `409 billing_conflict`.
- Reservation сохраняет `catalog_version`, `pricing_key` и обе ставки. Commit всегда использует этот snapshot, даже если active catalog успел измениться или удалить модель; те же audit fields пишутся в ClickHouse.
- Если provider вернул usage, commit использует его; иначе фактический output остается нулевым. Reservation при этом защищает лимит до commit/cancel/TTL.
- Tariffs и financial transactions пока не реализованы и fail closed при включении.

Локальный `docker-compose.yml` поднимает Redis, ClickHouse и PostgreSQL, но не микросервисы.

## Kubernetes deployment

Каждый компонент устанавливается отдельным Helm release в namespace `ai-gateway`. `charts/security` создает два Deployment/Service — DLP и AV.

```mermaid
flowchart TB
    User["Client"] -->|"LoadBalancer / port-forward"| GwSvc

    subgraph K8s["Kubernetes"]
        subgraph NS["namespace: ai-gateway"]
            GwSvc["Service ai-gateway-gateway :8080"] --> Gw["Deployment gateway"]
            AuthSvc["Service ai-gateway-auth :8082"] --> Auth["Deployment auth"]
            AnonSvc["Service ai-gateway-anonymizer :8081"] --> Anon["Deployment anonymizer"]
            BillingSvc["Service ai-gateway-billing :8083"] --> Billing["Deployment billing"]
            DlpSvc["Service ai-gateway-security-dlp :8084"] --> DLP["Deployment dlp"]
            AvSvc["Service ai-gateway-security-av :8085"] --> AV["Deployment av"]
            RedisSvc["Service ai-gateway-redis :6379"] --> Redis[("Redis")]
            ChSvc["Service ai-gateway-clickhouse :8123/:9000"] --> CH[("ClickHouse + PVC")]
            PgSvc["Service ai-gateway-postgres :5432"] --> PG[("PostgreSQL + PVC")]
        end
    end

    Gw --> AuthSvc
    Gw --> AnonSvc
    Gw --> BillingSvc
    Gw --> DlpSvc
    Gw --> AvSvc
    Billing --> ChSvc
    Gw -->|"rate limits + exact cache"| RedisSvc
    Billing -->|"atomic budgets + durable outbox"| PgSvc
    Anon -. "vault client not implemented" .-> RedisSvc
    DLP --> DlpIcap["External DLP ICAP"]
    AV --> AvIcap["External AV ICAP"]
    Gw --> Providers["External / host AI providers"]
```

В `devMode` workloads запускаются из примонтированного исходного дерева закрепленным образом `golang:1.26.7-alpine`. Директива `go` в модулях сохраняет language baseline Go 1.25, а сборка выполняется исправленным Go 1.26 toolchain. Для production следует отключить `devMode`, использовать собранные images и вынести credentials из values в управляемые Secrets.

## Основная конфигурация

```text
HTTP_ADDR=:8080
DEFAULT_PROVIDER=azure-open-ai
PROVIDERS_JSON=[...]

AUTH_REQUIRED=true
AUTH_URL=http://ai-gateway-auth:8082

DLP_REQUIRED=true
DLP_URL=http://ai-gateway-security-dlp:8084
AV_REQUIRED=true
AV_URL=http://ai-gateway-security-av:8085

ANONYMIZER_REQUIRED=true
ANONYMIZER_URL=http://ai-gateway-anonymizer:8081
ANONYMIZER_RULES=all

BILLING_REQUIRED=true
BILLING_URL=http://ai-gateway-billing:8083
```

Provider API keys не хранятся в `PROVIDERS_JSON`: chart создает Secret, а gateway подставляет ключ по переменной `PROVIDER_API_KEY_<NORMALIZED_ENDPOINT_NAME>`.

## Известные границы текущей реализации

- Нет Redis-backed anonymization vault; placeholder map request-local.
- PostgreSQL budgets/quotas реализованы; tariffs и financial transactions пока fail closed.
- Deanonymization не применяется к уже отправленным streaming chunks.
- Tool/function arguments входят в DLP/AV text projection и anonymization pipeline; JSON Schema инструмента не изменяется.
- DLP/AV сканируют текстовую проекцию запроса, а не произвольные бинарные вложения.
- Content rejection является terminal и не запускает fallback на другой endpoint.
- Budget rejection также terminal и возвращается как `429 budget_exceeded`.
- Gateway экспортирует OTLP/HTTP traces при заданном `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`, продолжает и проксирует W3C trace context и выполняет bounded graceful flush. Server/module/provider/client spans не содержат bearer, prompt или provider response.
- `/metrics` содержит HTTP, provider attempt, cache, module, security и billing lifecycle series. HTTP path/method и result нормализуются; identity, prompt, arbitrary model и secrets не используются как labels.
