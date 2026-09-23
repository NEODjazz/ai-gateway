# Конфигурация

Helm values — рекомендуемый интерфейс Kubernetes-конфигурации. Charts
преобразуют их в environment variables и Secrets. При локальном запуске те же
переменные задаются процессу напрямую. HTTP resources и payloads описаны в
OpenAPI, а не в этом документе.

## Gateway

| Environment variable | Default | Назначение |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | HTTP listener |
| `ADMIN_UI_ENABLED` | `true` | UI на `/ui/` |
| `API_DOCS_ENABLED` | `false` | Swagger UI и `/openapi.yaml` |
| `API_DOCS_TRY_IT_OUT_ENABLED` | `false` | Browser calls из Swagger UI |
| `DEFAULT_PROVIDER` | `PROVIDER_TYPE` или `demo` | Provider по умолчанию |
| `PROVIDERS_JSON` | пусто | Static provider endpoints; managed snapshot заменяет их после bootstrap |
| `MODEL_CATALOG_JSON` | empty catalog | Capabilities и pricing contract |
| `GUARDRAIL_POLICIES_JSON` | `{}` | Static DLP/AV policies |
| `GUARDRAIL_MONITOR_CAPACITY` | `1000` | Process-local monitor capacity, диапазон 1–10000 |
| `GUARDRAIL_MONITOR_TTL_SECONDS` | `604800` | Redis retention событий monitor |
| `ROUTING_STRATEGY` | `weighted` | `weighted` или `adaptive` |
| `ADAPTIVE_ROUTING_EWMA_ALPHA` | `0.2` | Сглаживание adaptive routing |
| `RESPONSES_AFFINITY_TTL_SECONDS` | `3600` | Affinity для `previous_response_id` |
| `RESPONSES_OWNERSHIP_TTL_SECONDS` | `2592000` | Срок хранения неизменяемой привязки сохраняемого Response к владельцу и deployment; требует Redis |
| `PROVIDER_CONTROL_PLANE_POSTGRES_DSN` | пусто | Durable versioned admin state |
| `PROVIDER_CREDENTIAL_ENCRYPTION_KEY` | ephemeral без DSN | AES-GCM key; с DSN требуется минимум 16 символов |
| `PROVIDER_CONTROL_PLANE_REFRESH_SECONDS` | `1` | Poll durable revision |
| `REDIS_ADDR` | пусто | Shared cache/rate/circuit/affinity/monitor state |
| `REDIS_DB` | `0` | Redis DB |
| `REDIS_PREFIX` | `ai-gateway` | Namespace Redis keys |
| `REDIS_PASSWORD` | пусто | Redis credential |

### Static provider endpoint

Элемент `PROVIDERS_JSON` объединяет connection и route в одной записи:

- identity: `name`, `type`, `base_url`, `api_key`, `models`, `model_aliases`,
  `enabled`;
- routing: `priority`, `weight`, `max_retries` и cooldown settings;
- admission: `max_parallel_requests`, `queue_capacity`, `queue_timeout_ms`;
- behavior: `stream`, `capabilities`, `rerank_path`;
- security: `dlp_enabled`, `av_enabled`, `guardrail_policy`;
- shadowing: `shadow`, `mirror_percentage`, `mirror_timeout_ms`.

`queue_capacity > 0` требует положительные `max_parallel_requests` и
`queue_timeout_ms`. Shadow endpoint требует `max_parallel_requests > 0`.
`mirror_percentage` должен быть от 0 до 100. `rerank_path` должен быть
абсолютным путём без query, fragment и `..`.
`rate_limit_rpm` и `rate_limit_tpm` задают deployment-level fixed-window quotas;
ноль означает отсутствие соответствующего ограничения. Допустимые максимумы —
10,000,000 RPM и 1,000,000,000 TPM. Те же поля у managed Provider задают
общий предел для всех ссылающихся deployments; оба scope применяются атомарно.

Поддерживаемые static adapter types: `demo`, `ollama`, `openai`,
`openai-compatible`, `openrouter`, `azure-openai`, `anthropic`, `gemini`,
`cohere`, `mistral`, `cerebras`, `nvidia-nim`, `together`. Capability задаётся явно для
ограниченных endpoints. Используемые значения: `chat`, `responses`,
`embeddings`, `rerank`, `stream`, `tools`, `structured_output`, `mcp`, `vision`,
`web_search`, `realtime`, `audio`, `audio_input`.
Capability names are exact and cannot be duplicated; deployment mutations reject
unknown or misspelled values.
Route выбирает endpoint только при наличии capabilities, выведенных из запроса. Moderation deployments должны явно указывать capability `moderation`; она не выводится из совместимого URL автоматически.

Provider API key в static config можно передать полем `api_key` или переменной
`PROVIDER_API_KEY_<NORMALIZED_ENDPOINT_NAME>`. Managed credentials шифруются в
control-plane snapshot и никогда не возвращаются read API.

### Managed control plane

Managed-режим намеренно разделяет конфигурацию на независимые ресурсы:

- Provider: `id`, `type`, `base_url`, shared RPM/TPM quotas и `enabled`;
- Credential: `id`, optional `provider_id`, description и write-only secret;
- Deployment: ссылки `provider_id`/`credential_id`, upstream/public models,
  capabilities, routing, admission, RPM/TPM quotas, retries, guardrail и enabled state;
- Model Group: public model ID, упорядоченные deployment IDs, strategy, retry
  policy и cross-model fallbacks;
- Model Catalog: pricing и model-level capabilities.

Provider и Credential должны быть созданы до ссылающегося Deployment, а
Deployment — до Model Group. UI использует выбор из уже созданных ресурсов, API
принимает их IDs и возвращает `409` при удалении используемого ресурса. Managed
Provider принимает `demo`, `ollama`, `openai`, `openai-compatible`,
`openrouter`, `azure-openai`, `anthropic`, `gemini`, `cohere`, `mistral`,
`voyage`, `bedrock`, `groq`, `deepseek`, `cerebras`, `nvidia-nim`, `together` и
`xai`.

`cerebras` использует bearer credential, обнаруживает модели через `/v1/models`
и поддерживает Chat Completions с streaming, function tools, JSON Schema output,
`reasoning_effort`, `logprobs`, `service_tier` и точным upstream usage. Нативное
поле ответа `reasoning` преобразуется в публичное `reasoning_content`. Остальные
неподтверждённые параметры отклоняются до отправки HTTP-запроса.

`nvidia-nim` поддерживает self-hosted endpoints без upstream credential и
hosted endpoints с bearer credential. Профиль публикует Chat Completions,
native Messages и count-tokens, legacy Completions, Responses
create/stream/retrieve/cancel,
Embeddings, native text Rerank и `/v1/models` discovery. Rerank принимает до
512 строковых passages, передает `truncate=NONE|END`, применяет `top_n` после
проверки полного результата и требует точный положительный provider token usage.
Входящий Messages-запрос сохраняет общий
policy, quota, retry и billing lifecycle, но отправляется в native endpoint.
Function tools, structured output и model-dependent image, audio и video input
доступны только через явно выбранные deployment capabilities.

`together` использует bearer credential и публикует только подтвержденные
Chat Completions, streaming, legacy Completions, Embeddings, Rerank,
Audio Transcription/Translation, Text-to-Speech и `/v1/models`. Rerank принимает
текстовые и объектные документы, `top_n` и `return_documents`; его учет требует
точного provider usage. Transcription и Translation принимают WAV, FLAC,
OGG/Opus, MP3, M4A/MP4 и WebM только при локально проверяемой длительности до
четырех часов; JSON-ответ ограничен и получает точный duration usage.
Translation передает prompt, response format и temperature, но отклоняет
неприменимые language и timestamp controls. Text-to-Speech принимает lowercase
language/locale, MP3, WAV и PCM, возвращает ограниченный бинарный ответ и
учитывает точное число Unicode-символов. Raw upstream streaming не публикуется
как SSE. Tools, structured output и vision задаются deployment capabilities.
Responses не публикуется, а параметры, которые upstream принимает без
применения, отклоняются до отправки запроса.

`GET /admin/v1/provider-capabilities` возвращает для каждого типа отдельно
реально реализованные операции адаптера, допустимые capabilities deployment,
явные значения `auth_type` и принятые runtime validator значения
`reasoning_effort`, `logprobs` и `service_tier`. Операция `count_tokens` публикуется только для
адаптеров с нативным счетчиком и не является выбираемой capability deployment.
Пустой `auth_types` означает фиксированный для адаптера способ передачи
credential.
Provider form загружает этот профиль и показывает только допустимые варианты
аутентификации для выбранного типа. Ошибка capability endpoint не блокирует
список и редактирование providers: форма использует встроенный безопасный набор.

Для `azure-openai` режим `auth_type=entra` использует статический bearer token
из привязанного write-only credential. Без credential gateway сначала проверяет
AKS workload identity через `AZURE_TENANT_ID`, `AZURE_CLIENT_ID` и абсолютный
`AZURE_FEDERATED_TOKEN_FILE`, затем локальные `IDENTITY_ENDPOINT` и
`IDENTITY_HEADER` App Service/Container Apps, затем Azure VM IMDS. Projected token
обменивается на scope `https://cognitiveservices.azure.com/.default` через
public-cloud Entra authority. Для endpoint с suffix `.openai.azure.us` или
`.cognitiveservices.azure.us` gateway автоматически использует authority
`https://login.microsoftonline.us` и resource
`https://cognitiveservices.azure.us/` как для federation, так и для managed
identity. Для endpoint с suffix `.openai.azure.cn` или
`.cognitiveservices.azure.cn` используются China authority
`https://login.chinacloudapi.cn` и resource
`https://cognitiveservices.azure.cn/`. Выбор sovereign cloud выполняется только
по полному host suffix; другие и похожие внешние домены остаются на public-cloud
defaults.
`AZURE_CLIENT_ID` также выбирает
user-assigned managed identity. Разрешены только loopback и link-local identity
endpoints; redirects и некорректные/просроченные ответы отклоняются. Временный
access token кэшируется и обновляется до истечения срока.
Azure Realtime использует GA URL `/openai/v1/realtime?model=...`, когда
`api_version` пуст, и preview URL
`/openai/realtime?api-version=...&deployment=...`, когда версия задана.
WebSocket handshake применяет тот же `api-key` либо Entra credential chain;
отсутствующий API key и пустой Entra token отклоняются до сетевого dial.

Для `gemini` режим `auth_type=api_key` использует write-only credential и header
`x-goog-api-key`. Режим `auth_type=gcp_adc` не требует привязанного credential:
gateway получает короткоживущий bearer token через фиксированный GCE metadata URL.
В GKE этот же запрос обслуживает Workload Identity Federation metadata server.
Gateway требует `Metadata-Flavor: Google` в запросе и ответе, запрещает redirects,
ограничивает размер и срок жизни ответа, объединяет параллельные refresh и
использует еще действующий token при кратковременной ошибке обновления.

Для managed provider `bedrock` значение `auth_type=aws_sigv4` требует `region`.
Write-only credential задается JSON-объектом с `access_key_id`,
`secret_access_key` и необязательным `session_token`. Если credential не привязан,
gateway последовательно проверяет `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`,
`AWS_ROLE_ARN` вместе с `AWS_WEB_IDENTITY_TOKEN_FILE`, выбранный
`AWS_PROFILE`/`AWS_DEFAULT_PROFILE` в shared credentials file, ECS container
credentials, EKS Pod Identity и EC2 IMDSv2. По умолчанию profile читается из
`$HOME/.aws/credentials`; `AWS_SHARED_CREDENTIALS_FILE` должен быть абсолютным.
Поддерживаются только статические `aws_access_key_id`, `aws_secret_access_key` и
необязательный `aws_session_token`; файл, строки, profile и значения имеют жесткие
лимиты, дубли и неполные credentials отклоняются. Web identity обменивается через региональный STS;
необязательный `AWS_ROLE_SESSION_NAME` задаёт имя сессии. Временные STS, container и
instance-role credentials кэшируются и обновляются до истечения срока действия;
параллельные запросы используют один refresh. Произвольный HTTP host в
`AWS_CONTAINER_CREDENTIALS_FULL_URI` отклоняется: разрешены только loopback и
стандартные link-local ECS/EKS addresses. Gateway подписывает каждый Converse
request для service `bedrock`, включая payload hash и временный session token.
Неверный JSON credential отклоняется при сохранении. `auth_type=bearer` сохраняет
прежний режим для частных совместимых endpoints.

## Gateway modules

| Модуль | URL | Required default | Особенность |
| --- | --- | --- | --- |
| Auth | `AUTH_URL` | `true` | Без URL доступна локальная реализация |
| DLP | `DLP_URL` | `true` | Remote-only; вызывается для endpoint с DLP enabled |
| AV | `AV_URL` | `true` | Remote-only; вызывается для endpoint с AV enabled |
| Anonymizer | `ANONYMIZER_URL` | `false` | Без URL доступна локальная реализация |
| Billing | `BILLING_URL` | `false` | `BILLING_SHARED_SECRET` защищает service contract |

Для каждого URL есть `<MODULE>_REQUIRED`. Optional dependency error позволяет
pipeline продолжить; content rejection остаётся terminal. Management API
использует отдельные `MANAGEMENT_AUTH_URL`, `MANAGEMENT_SHARED_SECRET`,
`BILLING_MANAGEMENT_URL` и `BILLING_MANAGEMENT_SHARED_SECRET`.
Gateway и соответствующий internal service должны получать одинаковый shared
secret. Это не клиентские Bearer-токены; auth management и billing management
должны использовать разные значения.

## Durable files

| Переменная | Default | Назначение |
| --- | --- | --- |
| `FILE_MAX_BYTES` | `33554432` | Максимальный размер одного multipart-файла; верхняя граница конфигурации 512 MiB |
| `FILE_OWNER_QUOTA_BYTES` | `1073741824` | Суммарная PostgreSQL-квота для пары credential/user; должна быть не меньше `FILE_MAX_BYTES` |
| `VECTOR_STORE_OWNER_QUOTA` | `1000` | Максимальное число неистекших vector stores для пары credential/user; допустимо от 1 до 100000 |
| `VECTOR_STORE_FILE_QUOTA` | `10000` | Максимальное число файлов в одном vector store; допустимо от 1 до 100000 |
| `VECTOR_STORE_BYTE_QUOTA` | `1073741824` | Атомарная квота суммарного размера активных файлов одного vector store; допустимо до 1 TiB |
| `ASSISTANT_OWNER_QUOTA` | `1000` | Максимальное число assistant definitions для пары credential/user; допустимо от 1 до 100000 |
| `CONVERSATION_OWNER_QUOTA` | `10000` | Максимальное число durable conversations для пары credential/user; допустимо от 1 до 1000000 |
| `CONVERSATION_ITEM_QUOTA` | `4096` | Максимальное число input/output items в одной conversation; допустимо от 1 до 100000 |
| `ASSISTANT_THREAD_OWNER_QUOTA` | `10000` | Максимальное число assistant threads для пары credential/user; допустимо от 1 до 1000000 |
| `ASSISTANT_MESSAGE_THREAD_QUOTA` | `100000` | Максимальное число сообщений в одном assistant thread; допустимо от 1 до 1000000 |
| `ASSISTANT_RUN_OWNER_QUOTA` | `10000` | Максимальное число сохраненных assistant runs для пары credential/user; допустимо от 1 до 100000 |
| `ASSISTANT_RUN_STEP_QUOTA` | `10000` | Максимальное число durable steps в одном assistant run; допустимо от 1 до 100000 |
| `ASSISTANT_RUN_RETENTION_SECONDS` | `2592000` | Retention assistant runs; допустимо от 60 секунд до 365 дней, истекшие записи удаляются при создании следующего run |

Files API возвращает `503`, если `PROVIDER_CONTROL_PLANE_POSTGRES_DSN` не
настроен. Квота сериализуется отдельно для каждого owner key и поэтому не
переполняется конкурентными загрузками.

## Cache и telemetry

| Переменная | Default |
| --- | --- |
| `EXACT_CACHE_TTL_SECONDS` | `0` (disabled) |
| `EXACT_CACHE_MAX_BYTES` | `1048576` |
| `SEMANTIC_CACHE_TTL_SECONDS` | `0` (disabled) |
| `SEMANTIC_CACHE_THRESHOLD` | `0.95` |
| `SEMANTIC_CACHE_MAX_ENTRIES` | `100` |
| `SEMANTIC_CACHE_MAX_BYTES` | `1048576` |
| `SEMANTIC_CACHE_EMBEDDING_URL` | required when enabled |
| `SEMANTIC_CACHE_EMBEDDING_MODEL` | required when enabled |
| `SEMANTIC_CACHE_EMBEDDING_API_KEY` | пусто |
| `OTEL_SERVICE_NAME` | `ai-gateway` |
| `AI_GATEWAY_VERSION` | `dev` |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | пусто (export disabled) |
| `OTEL_TRACE_SAMPLE_RATIO` | `1` |

## Auth

| Переменная | Default | Назначение |
| --- | --- | --- |
| `AUTH_POSTGRES_KEYS_ENABLED` | `false` | Durable Virtual Keys и directory |
| `AUTH_POSTGRES_DSN` | пусто | Auth PostgreSQL |
| `AUTH_KEY_HASH_SECRET` | пусто | HMAC pepper для key lookup |
| `AUTH_STATIC_KEY_FALLBACK_ENABLED` | `true` | Разрешить `AUTH_VIRTUAL_KEYS_JSON` |
| `AUTH_DEMO_KEYS_ENABLED` | `true` | Built-in demo keys |
| `AUTH_VIRTUAL_KEYS_JSON` | пусто | Static migration fallback; содержит plaintext keys |
| `MANAGEMENT_SHARED_SECRET` | пусто | Защита auth `/internal/v1/*` |
| `AUTH_JWT_SECRET` | пусто | Legacy HS256 secret |
| `AUTH_JWT_JWKS_URL` | пусто | OIDC JWKS; включает RS256/ES256 mode |
| `AUTH_JWT_ISSUER` / `AUTH_JWT_AUDIENCE` | пусто | Ожидаемые claims в JWKS mode |
| `AUTH_JWT_JWKS_CACHE_TTL_SECONDS` | `300` | JWKS cache TTL |
| `AUTH_JWT_CLOCK_SKEW_SECONDS` | `30` | Допустимый clock skew |
| `AUTH_JWT_USER_ID_CLAIM` | `sub` | Dot-separated claim path |
| `AUTH_JWT_TEAM_ID_CLAIM` | `team_id` | Dot-separated claim path |
| `AUTH_JWT_ROLES_CLAIM` | `roles` | Dot-separated claim path |

Production должен использовать уникальные `AUTH_KEY_HASH_SECRET` и management
secret, отключённые demo keys и static fallback после миграции ключей.

## Billing

| Переменная | Default | Назначение |
| --- | --- | --- |
| `BILLING_USAGE_EVENTS_ENABLED` | `false` | ClickHouse usage events |
| `CLICKHOUSE_URL` | `http://localhost:8123` | ClickHouse HTTP endpoint |
| `CLICKHOUSE_DATABASE` | `ai_gateway` | Database |
| `CLICKHOUSE_USAGE_EVENTS_TABLE` | `usage_events` | Event table |
| `CLICKHOUSE_USERNAME` / `CLICKHOUSE_PASSWORD` | пусто | ClickHouse credential |
| `POSTGRES_DSN` | пусто | Budgets, audit и durable outbox |
| `BILLING_SHARED_SECRET` | пусто | Защита `/usage` |
| `BILLING_MANAGEMENT_SHARED_SECRET` | пусто | Защита billing `/internal/v1/*` |
| `BILLING_DURABLE_OUTBOX_ENABLED` | `false` | PostgreSQL-backed delivery |
| `BILLING_OUTBOX_POLL_MS` | `500` | Worker polling interval |
| `BILLING_RESERVATION_TTL_SECONDS` | `900` | Active budget reservation TTL |
| `BILLING_DEFAULT_RESERVE_OUTPUT_TOKENS` | `1024` | Fallback output allowance |
| `BILLING_LIMITS_ENABLED` / `BILLING_QUOTAS_ENABLED` | `false` | Atomic budget enforcement |
| `BILLING_TARIFFS_ENABLED` / `BILLING_FINANCIAL_TRANSACTIONS_ENABLED` | `false` | Зарезервировано; fail closed |

ClickHouse username/password и service/management shared secrets должны
приходить из Secret. Gateway и Billing должны получать одинаковые catalog JSON
и billing service secret. Management secret является отдельным credential.

## Anonymizer, DLP и AV

Anonymizer слушает `:8081`. `ANONYMIZER_RULES` выбирает built-in правила;
`ANONYMIZER_RULES_CONFIG_PATH` загружает JSON definitions с `name`,
`placeholder`, RE2 `pattern`, optional `capture_group` и `validator=luhn`.
`exclude_values` задаёт регистронезависимый список значений capture group,
которые правило должно оставить без изменений. Для кириллических слов нужны
явные Unicode-границы: RE2 `\b` использует ASCII word characters.

DLP и AV используют `HTTP_ADDR` (`:8084` и `:8085`), `<MODULE>_ICAP_HOST`,
`<MODULE>_ICAP_PORT`, `<MODULE>_ICAP_SERVICE` (`/dlp` или `/av`) и
`<MODULE>_ICAP_TIMEOUT` (`5s`). Generic `ICAP_*` служат fallback. Пустой ICAP
endpoint не мешает startup, но непустой scan завершится dependency error.

## Helm secrets

Default values удобны только для локального запуска. Для managed окружения
передавайте secrets через защищённый values source или заранее созданные
Kubernetes Secrets. Не храните реальные provider keys, DSN passwords,
encryption keys и shared secrets в Git. Изменение encryption key без
перешифрования snapshot сделает сохранённые credentials нечитаемыми.
