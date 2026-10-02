# Требования к следующему этапу AI Gateway

Дата оценки: 2026-10-01. Базовая ревизия: `e70d92cc`.

Цель этапа — дать пользователям доступ к моделям через Keycloak и внешний
OpenWebUI с достоверными правами, лимитами, ownership и billing. Переписывание
сервисов и расширение каталога без конкретного потребителя в этот этап не входят.

Разделы требований ниже фиксируют оценку базовой ревизии. Результаты реализации
и сквозных проверок приведены отдельно. Наличие handler и успешный fixture-тест
не доказывают полную совместимость каждой внешней модели или провайдера.

## Результат реализации P1, 2026-10-01

| Поставка | Статус и доказательства |
| --- | --- |
| A. Identity/policy/ownership | Реализовано: opt-in directory JWT, immutable binding, stable credential, текущие grants/membership/organization/limits, изоляция cache/resources/billing; unit и реальные PG regressions |
| B. Keycloak | Реализовано: secret-free realm/clients/mappers, явный provisioning/roles, реальные rotation/refresh/deprovision/logout/expiry/failure/recovery tests |
| C. OpenWebUI | Реализовано: System OAuth, Authorization Code + PKCE, server refresh, разные `/openai/models` и `/api/models`, полный JSON/SSE chat, отсутствие session fallback, 401/429 и billing attribution |
| D. CI/release/local rollout | Обязательные PostgreSQL и real identity jobs прошли в GitHub Actions; [релиз 0.3.2](https://github.com/NEODjazz/ai-gateway/releases/tag/v0.3.2) опубликован со всеми шестью образами и восемью Helm-пакетами. Local rollout выполнен: migration 013 применена, шесть application deployments на 0.3.2 и все девять deployments Ready, ingress/UI и Auth management API проверены; Secret data и runtime settings сохранены |

Поставки A–D завершены. [Release workflow](https://github.com/NEODjazz/ai-gateway/actions/runs/36914315449)
и оба CI runs релизного commit завершились успешно. Опубликованный PostgreSQL
chart содержит проверенный SQL в ConfigMap data и прошёл strict server dry-run.

Реализация и переход описаны в [профиле Keycloak/OpenWebUI](identity-keycloak-openwebui.md).
[Сквозной сценарий](../scripts/test-identity-integration.py) использует реальные
Keycloak 26.3.3, OpenWebUI 0.11.4, Auth/Gateway/Billing и PostgreSQL; provider —
детерминированный HTTP fixture. Реальные JWT проверяют stable identity после
refresh, разные grants/quotas/resources/cache и spend attribution. Directory SQL
failure и недоступность IdP/JWKS закрывают доступ и допускают восстановление.
12 concurrent requests — regression workload, а не production SLO benchmark.

Background Responses/Interactions cancellation ждёт terminal usage перед
settlement, а queued batch повторно проверяет directory policy; подтверждённый
отзыв и временная недоступность directory имеют разные результаты. Эти пути и
повторная доставка billing events проверяются unit/PG regression tests в CI.
Browser проверка подтверждает реальный login и видимый отказ неназначенного tool.
OpenWebUI built-in tools требуют явных grants либо отключения для plain chat.
Для OpenWebUI 0.11.4 используется статический operator-owned registry и
пользовательские UI ACL: разные OAuth-зависимые каталоги не заменяют общий
registry. Чередующиеся и параллельные чаты получают разрешённые модели;
Gateway остаётся enforcement point даже при устаревшем внешнем ACL.

Прошли `gofmt -w .`, `go vet ./...`, `go test ./...`, `go test -race ./...`,
`go build ./...` во всех шести modules на Go 1.25.13, обязательные PostgreSQL
сценарии, 181 UI tests/build, production npm audit, Helm lint/render всех charts
и OpenAPI/documentation contracts. Уязвимая transitive зависимость brace-expansion
обновлена отдельно, прямые UI packages и Go dependencies не менялись.

При local rollout выявлено неверное размещение migration 013 в Helm ConfigMap
metadata вместо data. Chart исправлен; regression воспроизводит прежний дефект,
обязательный PG runner требует эту миграцию. Исправленный manifest прошёл strict
server dry-run Kubernetes, полные Gateway vet/test/build и fresh PostgreSQL/race
проверки Gateway/Auth/Billing. Существующая local БД получила additive migration
транзакционно. После разрешённой очистки завершённых fixture containers и старого
build cache DiskPressure снят; рабочие volumes и Secret data не менялись.
Local JWT mode сохранён legacy: production trust и provisioning остаются opt-in.
Local rollout обновил images/version Deployment и migration ConfigMap.
Существующий Helm release state не перезаписывался: его values содержали
устаревший provider Secret. Перед следующим Helm upgrade оператору нужно
согласовать актуальные Secret references/values; live Secret data сохранены.

Production IdP trust не включён автоматически. До opt-in нужны рабочие HTTPS
endpoints, confidential client secrets, источник provisioning и назначенные
users/bindings. Legacy credentials и fingerprint-owned resources сохраняются;
массового переноса по email нет. Поставка E расширенного admin browser SSO,
полный RAG pipeline и внешние cloud/provider interoperability scenarios остаются
отдельными продолжениями, а не скрыто заявленным результатом этих тестов.

## Существующая основа

- JWT: RS256/ES256, JWKS, issuer/audience/expiry validation, настраиваемые
  вложенные claims. См. [jwt.go](../repos/auth/internal/modules/jwt.go).
- Browser sign-in: authorization code с PKCE, проверка state, зашифрованная
  cookie. См. [browser_sso.go](../repos/gateway/internal/gateway/browser_sso.go).
- Users/teams/organizations, SCIM Users/Groups, virtual keys, access groups,
  model/tool grants и административный RBAC уже реализованы.
- Budget scopes: global, organization, team, user, key, model, provider,
  deployment, tag. PostgreSQL reservation/lifecycle/outbox и ограниченные
  memory stores уже есть; это не новые задачи этапа.
- Providers/Credentials/Deployments, service principal для Azure Entra,
  capability profiles и onboarding доступны в admin UI.

Подробности: [API coverage](api-coverage.md), [надёжность](production-reliability.md),
[безопасность](security.md), [конфигурация](configuration.md).

## Приоритет P1: identity и права пользователей

Подтверждённые ограничения JWT-пути в базовой ревизии `e70d92cc`:

1. [authorizeJWT](../repos/auth/internal/modules/auth.go) заполняет user, team,
   roles и fingerprint токена. Он не загружает directory status, organization,
   access groups, model/tool grants или RPM/TPM из управляемого каталога.
   [modelAllowed](../repos/gateway/internal/gateway/access.go) разрешает все модели
   при пустом списке grants. Подключение IdP само по себе не переносит права
   directory в inference.
2. `CredentialID` — fingerprint полного JWT. После refresh он меняется.
   [fileOwnerKey](../repos/gateway/internal/gateway/files.go) и
   [conversation OwnerKey](../repos/gateway/internal/conversationstate/store.go)
   зависят от credential и user. Обновление JWT меняет ключ владения;
   [rate-limit key](../repos/gateway/internal/gateway/access.go) также зависит от
   credential. Это вывод из функций ключевания, а не результат live SSO-теста.
3. User ID берётся из `sub`; issuer сохраняется только в metadata. Для будущего
   расширения на несколько issuer потребуется явное пространство identity.
   Сейчас verifier настроен на один issuer; конфликт разных issuer в этом
   режиме не заявляется как воспроизведённая ошибка.

Требования к реализации:

- Связывать внешний `(issuer, sub)` со стабильным внутренним principal ID.
  Не связывать аккаунты автоматически по email. Сохранять отдельный credential
  ID для virtual keys и явно определить application scope для JWT identity.
- После проверки подписи загружать active status, подтверждённое membership,
  organization, grants, access groups, tags и лимиты. Не принимать membership
  или роль администратора из произвольного клиентского заголовка.
- Для нового JWT-принципала выбрать явный режим: предварительное provisioning
  либо разрешённый оператором JIT с ограниченными default grants. Неизвестный
  пользователь без разрешённого режима получает отказ.
- Отдельно определить отличие «модели не назначены» от существующего значения
  пустого списка «без ограничения». Не менять семантику существующих keys.
- Keycloak client roles отображать по явной таблице в gateway roles;
  group-to-team/access-group mapping управляет оператор. Отсутствие подходящего
  mapping не должно выдавать административные права.
- Refresh JWT сохраняет ownership, квоты, user budget, attribution и affinity.
  Cache сохраняет разделение между пользователями и effective policy; изменение
  membership/grants должно изменить cache scope и доступ к новой execution.
- Disabled/deleted user не авторизуется через JWT только потому, что его подпись
  ещё действительна. Для фоновых jobs определить reauthorization/cancellation
  и settlement при отключении пользователя.
- Старые ресурсы с ownership на fingerprint JWT не переносить массово без
  проверенного соответствия principal; определить переходный режим и rollback.

Приёмка: два разных JWT одного principal до/после refresh видят его файлы и
Conversations, расходуют один user budget и применимые лимиты; другой principal
не видит эти ресурсы. Disabled user и неверные issuer/audience/role/membership
отклоняются. Проверить модели, tools, cache, SSE, background jobs и PostgreSQL
reserve/settlement, включая отказ directory и повторную доставку billing event.

## Приоритет P1: Keycloak

Первый вариант использует имеющийся JWT verifier и явные endpoints. Для одного
realm нужны issuer, JWKS URL, audience ресурса Gateway и согласованные claims.
Client роли удобно размещать в `resource_access.<client-id>.roles`, что уже
поддерживает текущий dotted-path reader. Имена role values требуют mapping.

Разделить OIDC clients внешнего OpenWebUI и административной консоли Gateway.
Access token для inference должен иметь audience Gateway; ID token и локальный
session token внешнего интерфейса не являются заменой такого access token.
Keycloak audience mapper настраивается отдельно от проверки `aud` в Gateway.

Нужно подготовить пример realm/client/mapper без секретов, инструкции по
provisioning, claim/role mapping и регрессионные тесты. Не считать существующие
SCIM endpoints доказательством, что Keycloak штатно отправляет SCIM provisioning;
источник Users/Groups и способ синхронизации должны быть выбраны отдельно.

Расширение административной SSO-сессии:

- Текущий `browserSSOMaxTokenBytes=2800` ограничивает group-rich access tokens;
  cookie содержит сам access token. Если такие tokens нужны, использовать
  ограниченное серверное session storage и opaque cookie, а не увеличивать
  cookie без учёта браузерного лимита.
- Сейчас нет refresh flow; cookie живёт не дольше access token.
- Сейчас logout очищает локальную cookie. Требования к RP-initiated logout,
  back-channel logout или introspection зависят от согласованного срока
  принудительного отзыва; локальная JWKS-проверка не узнаёт об отзыве мгновенно.
- Discovery и управление IdP через UI можно добавить поверх явной configuration.
  Изменения issuer/audience/JWKS относятся к настройкам доверия: нужны write-only
  secrets, audit, проверка endpoints, защита от lockout и rollback.

Приёмка: реальный тестовый realm, два пользователя с разными grants, signing-key
rotation, refresh, отключение пользователя, logout, ошибки IdP и восстановление.
Дополнительно проверить согласованный maximum token size и административную
роль; inference user не получает доступ к административным операциям.

Официальные контракты: [Keycloak OIDC](https://www.keycloak.org/securing-apps/oidc-layers).

## Приоритет P1: внешний OpenWebUI

Предпочтительный flow:

`User → OpenWebUI → Keycloak access token → Gateway → provider`.

В проверенном исходном коде OpenWebUI connection auth mode `system_oauth`
получает исходный access token из серверной OAuth session и передаёт его в
`Authorization: Bearer`. Режим `session` передаёт локальный token OpenWebUI,
а `bearer` — ключ connection. Это три разных режима авторизации.

Требования:

- Подключить Keycloak OIDC к OpenWebUI; connection указывает на Gateway `/v1`
  и использует исходный OAuth access token с нужным audience.
- Согласовать refresh scopes и encrypted OAuth-session storage на стороне
  OpenWebUI; отсутствие/ошибка refresh не должно вызывать fallback на более
  привилегированный общий ключ.
- Gateway самостоятельно проверяет JWT и назначает policy по подтверждённой
  identity. `X-OpenWebUI-*`, request `user` и metadata пригодны для корреляции,
  но не должны подменять аутентифицированного пользователя.
- Не доверять клиентскому списку моделей. Проверить отдельно `/v1/models`,
  кеширование model list в OpenWebUI и прямой вызов скрытой модели. Gateway
  остаётся enforcement point даже при устаревшем списке внешнего интерфейса.
- Проверить JSON/SSE, tools и tool results, attachments, embeddings/RAG по
  согласованному сценарию, disconnect/cancellation, 401/403/429, budget errors,
  пользовательские usage/spend и фоновые запросы названий/summary.
- Background requests без пользовательской OAuth session требуют отдельно
  ограниченного service principal/virtual key; не приписывать их человеку без
  подтверждённой делегации. Такое использование не входит в базовый flow.
- Проверить installed version и доступность соответствующего режима в её UI.
  Если исходный OAuth token не может быть передан, альтернативой служат отдельные
  пользовательские virtual keys; federation bridge/token exchange оценивается
  отдельно. Общий connection key не обеспечивает per-user enforcement.

Приёмка: end-to-end два пользователя → разные видимые/разрешённые модели,
раздельные budgets/resources/cache, корректные spend records после refresh,
отказ для подменённых headers и просроченных tokens. Backend denial корректно
отображается во внешнем UI. Зафиксировать version/configuration и сценарии в CI.

Источники настройки: [OpenWebUI SSO](https://docs.openwebui.com/features/authentication-access/auth/sso/),
[Keycloak для OpenWebUI](https://docs.openwebui.com/features/authentication-access/auth/sso/keycloak/).

## API: существующие операции и целевые продолжения

Все строки ниже имеют зарегистрированный handler в
[routes.go](../repos/gateway/internal/gateway/routes.go), включая отдельную
регистрацию GenerateContent. Таблица перечисляет 38 семейств; Models, Audio
translation, Conversations, Messages batches и Vector-store file batches
дополнительно описаны в [API coverage](api-coverage.md).

«Есть» означает реализованную ограниченную операцию. Расширения P2/P3 запускаются
при наличии бизнес-сценария и provider contract; они не блокируют identity этап.

| Семейство | Текущий entry point | Целевое продолжение | Приоритет |
| --- | --- | --- | --- |
| Chat completions | `/v1/chat/completions` | Проверенные model-specific parameters, tools/usage compatibility | P2 |
| Responses | `/v1/responses` | Дополнительные native parameters/counters | P2 |
| Responses compact | `/v1/responses/compact` | Native options выбранных провайдеров | P3 |
| Messages | `/v1/messages` | Дополнительные server tools и live skill streaming | P3 |
| Messages count tokens | `/v1/messages/count_tokens` | Native blocks/counters выбранных провайдеров | P2 |
| GenerateContent | `/v1beta/models/{modelAction}` | Дополнительные remote MCP transports | P3 |
| Interactions | `/v1/interactions` | Stream resumption, environment sources, network/secret controls | P3 |
| Text completions | `/v1/completions` | Дополнительные native controls по спросу | P3 |
| Embeddings | `/v1/embeddings` | Provider input/usage matrices | P2 |
| Rerank | `/v1/rerank` | Provider input/usage matrices | P2 |
| Image generation | `/v1/images/generations` | Выбранные native adapters | P3 |
| Image edits | `/v1/images/edits` | Native mask editing выбранных adapters | P3 |
| Image variations | `/v1/images/variations` | Выбранные native adapters | P3 |
| Audio transcription | `/v1/audio/transcriptions` | Выбранные adapters и pricing contracts | P3 |
| Text to speech | `/v1/audio/speech` | Выбранные adapters и pricing contracts | P3 |
| Realtime | `/v1/realtime` | Отдельные ASR dimensions, native protocols/WebRTC/SIP по спросу | P3 |
| Videos | `/v1/videos` | Provider lifecycle, cancellation, priced dimensions | P3 |
| OCR | `/v1/ocr` | Выбранные native adapters | P3 |
| Moderation | `/v1/moderations` | Выбранные native adapters | P2 |
| Apply guardrail | `/guardrails/apply_guardrail` | Дополнительные policy-visible scanners | P2 |
| Files | `/v1/files` | Object storage, purpose-specific retention | P2 |
| Batches | `/v1/batches` | Другие item families и native batch transports | P3 |
| Fine tuning | `/v1/fine_tuning/jobs` | Async reconciliation окончательной training cost | P2 при использовании |
| Assistants | `/v1/assistants`, `/v1/threads` | Hosted runtime только по отдельной потребности | P3 |
| Search | `/v1/search` | Provider adapters и tiered pricing | P3 |
| Skills | `/v1/skills` | Native contracts и incremental execution | P3 |
| MCP | `/v1/mcp/servers/{id}/tools` | Credential delegation, sampling/roots/elicitation по спросу | P3 |
| A2A | `/a2a/{agent}` | Binary parts и выбранные agent contracts | P3 |
| Bedrock Invoke | `/model/{model}/invoke` | Дополнительные model dialects | P3 |
| Bedrock Converse | `/model/{model}/converse` | Дополнительные native content blocks | P3 |
| Containers | `/v1/containers` | Runtime contracts, квоты и pricing | P3 |
| Container files | `/v1/containers/{id}/files` | Runtime contracts и retention | P3 |
| Sandbox | `/v1/sandbox/execute` | Runtime contracts и priced dimensions | P3 |
| Vector-store creation | `/v1/vector_stores` | Durable background ingestion/indexing | P2 при использовании |
| Vector-store files | `/v1/vector_stores/{id}/files` | Background parsing и binary documents | P2 при использовании |
| Vector-store search | `/v1/vector_stores/{id}/search` | Precomputed indexes и большие stores | P2 при использовании |
| RAG ingestion | `/v1/rag/ingest` | Background parsing/indexing и binary documents | P2 при использовании |
| RAG query | `/v1/rag/query` | Precomputed indexes, claim verification, большие corpora | P2 при использовании |

## Операционные требования после identity этапа

| Область | Существующая основа и ограничение | Решение / возможность | Приоритет |
| --- | --- | --- | --- |
| Provider catalog | 20 managed types, включая demo, compatible transports и sandbox; это не 20 равноценных native LLM adapters | Добавлять конкретный adapter с native auth, streaming и usage contract tests | P2 по спросу |
| Azure/cloud auth | Entra SP, managed/workload identity, AWS SigV4/STS, GCP metadata есть | Дополнительные credential chains только для выбранных облаков | P3 |
| Tokenization | Общая оценка контекста/tools и ряд native counters | Versioned model tokenizer/counter, provenance и reserve fallback | P2 |
| Catalog/pricing | Versioning, discovery/hot update есть; нет общего scheduled verified upstream sync | Preview, trusted sources, price provenance, atomic apply и rollback | P2 |
| Parameter compatibility | Strict decoding и machine-readable capabilities есть | Расширять по model/provider и API family; unsupported остаётся явной ошибкой | P2 |
| Routing | Priority/weight, adaptive EWMA, retries, cooldown, fallback, affinity, admission есть | Cost/least-busy/semantic classifiers и priority reservations только по измеренной потребности | P3 |
| Cache/billing | Memory bounds, policy/user isolation, durable outbox и execution ID есть | Нагрузочные SLO и failure recovery для JWT identity; обязательный PG CI сохраняется | P1 в identity этапе |
| Projects | Project/team/access-group metadata есть | Настоящий project budget/spend/owners, без подмены project scope неявным tag | P2 |
| Budgets | Девять independent scopes есть; составного key+model scope и temporary adjustment нет | Composite scope, expiring adjustments и soft-budget notifications | P2 |
| Delegated administration | Global admin и scoped team directory есть | Явный org/project/team admin permission matrix для CRUD, reports и models | P2 |
| SCIM | Users/Groups lifecycle есть | Реальный provisioning source, JWT-linking и deprovision enforcement | P1 в identity этапе |
| Secrets | Encrypted write-only credentials и explicit rotation есть | Выбранный external secret manager, refresh/cache/failure policy; scheduled rotation отдельно | P2 по спросу |
| Logging | OTel/metrics, request logs/audit, HTTPS webhooks есть; webhook registry/queue memory-only | Durable dispatch, bounded/cancellable worker, team routing/redaction и выбранный log backend | P2 |
| Retention/export | Request-log retention setting есть; это не доказательство полного audit lifecycle | Audit retention enforcement и object-storage export с retry/restore checks | P2 |
| Guardrails | DLP/AV/anonymization rules, input/output DLP и effective policies есть | Выбранные semantic/output scanners, timeout/failure semantics и tests | P2 по спросу |
| Network access | Authentication/RBAC и service separation есть; встроенный клиентский CIDR ACL не найден | Ingress enforcement либо explicit trusted-proxy/CIDR policy по требованию | P3 |
| Passthrough | Произвольный passthrough не реализован | Route allowlist, auth, owner isolation, accounting и bounds | P3 |
| Multi-region | Сервисы, Redis/PG/ClickHouse и Helm есть | Выбрать consistency model, DR/RPO/RTO, migration discipline и failover tests | P3 |
| Prompt registry/evals | Отдельный versioned prompt-management/evaluation subsystem не найден | Интегрировать выбранный внешний backend при реальном workflow | P3 |
| User interface | Admin control plane/Playground есть | Пользовательские chats через внешний OpenWebUI; branded catalog/self-service отдельно | P1 / P3 |
| SDK/support | Gateway — Go HTTP services, без универсального in-process Python SDK | HTTP contract/SDK generation по спросу; support SLA требует процесса эксплуатации | Вне identity этапа |

Для условных работ «P2 по спросу» отсутствие бизнес-потребности означает
отложенную реализацию. Наличие интеграции в другой системе не является само по
себе причиной добавлять её в Gateway.

## Трудоёмкость и порядок поставки

Оценки — мои активные часы разработки, диагностики и проверок на текущем коде,
а не календарное обещание. Они включают regression tests и focused commits.
Ожидание доступа к IdP, cloud permissions, согласований и CI очереди не включено.
Диапазоны учитывают изменения межсервисных DTO, миграции и failure cases.

| Поставка | Что входит | Активные часы |
| --- | --- | --- |
| A. Стабильная JWT identity и policy binding | Principal mapping, directory enforcement, grants/limits, ownership/cache/billing transition; unit и PG regressions | 14–24 |
| B. Keycloak профиль | Realm/client/mappers example, явные roles/groups, provisioning instructions, JWKS/claim tests | 6–10 |
| C. OpenWebUI integration | User OAuth-token connection, model-list isolation, JSON/SSE/tools, refresh/errors и per-user spend сценарии | 8–14 |
| D. Общая проверка и релиз | Реальные тестовые Keycloak/OpenWebUI/PG, два пользователя, failure/load cases, CI, review и local rollout | 4–8 |
| **Первый приоритетный релиз A–D** | Зависимости: A → B/C → D | **32–56** |
| E. Расширение admin browser SSO | Server-side sessions, refresh, согласованный logout/revocation; tests | 8–16 |
| **Identity релиз с расширенным admin SSO** | A–E | **40–72** |

Первые commit boundaries: principal mapping → directory/grants → ownership
transition → Keycloak profile → OpenWebUI connection/tests → CI/release.
Каждый behavior change получает собственные regressions и commit. При изменении
concurrency — race detector. PG-сценарии проверяются на disposable test databases.

Ориентиры для отдельно выбранных продолжений:

| Продолжение | Единица оценки | Часы |
| --- | --- | --- |
| Model tokenization | Одно выбранное семейство моделей с provenance и reserve tests | 6–12 |
| Pricing sync | Один trusted source, preview/apply/rollback и scheduled refresh | 8–16 |
| Governance budgets | Project/composite scope, temporary adjustment и soft alerts | 16–30 |
| Delegated admin | Согласованная org/team/project permission matrix | 12–24 |
| Secrets | Один выбранный external backend с refresh/failure tests | 6–12 |
| Logging/export | Durable delivery, tenant policy и один backend/export target | 12–24 |
| Provider adapter | Один обычный API family с native auth/JSON/SSE/usage | 4–12 |
| Complex runtime/API | Один realtime/media/async/MCP/RAG сценарий | 12–32 |

Это отдельные ориентиры, а не сумма обязательного backlog. Массовое добавление
providers, всех native options или multi-region требует отдельного перечня
операций и SLO; достоверную итоговую оценку без него дать нельзя. После A–D
переоценить выбранные P2 на измерениях реального workload.

## Критерий завершения этапа реализации

Keycloak user входит в OpenWebUI и вызывает только назначенные модели/tools;
Gateway применяет его актуальные права и бюджеты. Refresh сохраняет identity,
ownership и spend attribution. Другой пользователь не получает его данные или
cache. Отключение пользователя и отказ IdP/directory не дают более широкого
доступа. SSE и ошибки отображаются корректно. Обязательные regression/integration
tests проверяют это с реальными test services и PostgreSQL в CI.

Этот документ завершает оценку и планирование. Он не заявляет, что перечисленные
расширения уже реализованы или что live interoperability уже подтверждена.

## Проверки исходной оценки

В базовом исходном коде проверена регистрация и Go definition каждого из 38
handler families; проверены локальные ссылки документа и отсутствие посторонних
продуктовых ссылок в новых требованиях. Выполнены `gofmt -w .`, `go vet ./...`,
`go test ./...`, `go build ./...` во всех шести Go modules на Go 1.25.13.
Все команды завершились успешно; runtime-код не менялся.

PostgreSQL integration отдельно не запускались: test DSN не предоставлены.
Race detector, UI component/browser tests, container build и live
Keycloak/OpenWebUI interoperability в этой документальной оценке не запускались.
Это обязательные проверки соответствующих изменений на этапе реализации,
а не доказательство уже работающей внешней интеграции. Deployment и настройки
авторизации этой оценкой не изменены.

## SSO и доступ нескольких организаций: оценка 2026-10-02

Статус реализации: первый этап managed browser SSO отделяет API trust, проверяет
ID token/nonce/PKCE и использует bounded PostgreSQL sessions с revocation и
актуальными directory grants. Старое managed API trust переносится с CAS;
browser client audience и resource audience разделены. Требуется migration 015
и повторная binding/test для managed профилей прежнего access-token flow.
Unit/race и полный PostgreSQL integration suite прошли; реальный Keycloak
callback, logout/replay, rollback, независимость API и OpenWebUI проверены.
Environment-only legacy browser flow остаётся переходным контрактом, описанным
в [SSO настройках](admin-sso-settings.md).

Следующий этап добавляет [явное членство и tenant identity](organization-identity.md):
роль `org_admin` требует approval именно в организации binding, user/organization
ownership JWT principal закреплены, sessions и durable jobs повторно проверяют
актуальные права. Migration 016 не назначает административные роли и не переносит
ресурсы; старые tenant-bound sessions требуют нового входа. Делегированные
resource permissions/UI, проверенный выбор tenant, несколько connections/API
issuers и новый SSO UI ещё не завершены.

Основа этой дополнительной оценки — код Gateway на commit `4ded04d1`.
Ниже перечислены требования следующего этапа, а не уже реализованные возможности.
Browser SSO, доверие API JWT и изоляция организаций — отдельные контракты; наличие
нескольких организаций в directory само по себе не означает несколько SSO
подключений или полную изоляцию всех management endpoints.

### Подтверждённое состояние

| Область | Текущее поведение | Требуемое изменение |
| --- | --- | --- |
| Browser SSO и API JWT | `SSOManager.JWTModule` заменяет глобальный issuer/audience/role policy активным SSO-профилем; выключение browser login сохраняет это доверие | Независимые browser connections и API JWT trust; редактирование browser login не меняет доступ API-клиентов |
| Хранение SSO | PostgreSQL singleton содержит один active/draft/previous profile и одну test attempt | Несколько bounded connections при потребности в нескольких IdP/организациях; независимые revision, proof и rollback |
| OIDC endpoints | Validator и discovery требуют один origin для issuer, authorization, token и JWKS | Trusted discovery/presets с явной endpoint policy, допускающей разные доверенные hosts; сохранить HTTPS, SSRF и redirect protections |
| Browser identity | Callback использует access token; необходим JWT для Gateway resource audience, ID token отдельно не обрабатывается | Browser OIDC identity через проверенный ID token и локальную сессию; API по-прежнему проверяет resource access token |
| Сессии | Encrypted cookie содержит access token до 2800 байт; lifetime ограничен token expiry, refresh отсутствует | Bounded server-side sessions с revocation и проверкой актуального directory; refresh только по отдельному требованию |
| UI настройки | Один generic form, role mappings в JSON, ручная binding точного subject; draft/test/activate/disable/rollback есть | Provider presets, визуальные mappings, безопасная диагностика identity и список подключений при их появлении |
| Организации | Organizations/teams, organization budget/spend и organization в cache isolation существуют | Явно определить Organization как tenant boundary и проверить каждый management/data endpoint до объявления полной изоляции |
| Principal и права | Namespace `issuer + subject + audience`, approved directory roles и team binding уже есть; global admin и scoped team admin | Сохранить namespace и актуальные права; добавить отдельный org admin и проверяемый выбор доступной организации/команды |
| Ownership ресурсов | Files и связанные owner-scoped APIs используют user + credential; смена organization при тех же значениях не меняет owner key | До добавления переключения tenant определить tenant namespace и совместимый переход для существующих ресурсов |

Последняя строка описывает ограничение будущего переключения контекста, а не
доказанную утечку между текущими пользователями. Namespace JWT principals уже
различает одинаковые subjects разных issuers. Изоляцию новых сценариев нельзя
считать подтверждённой только на основании directory, budgets или cache keys.

Источники текущего контракта: [SSO настройки](admin-sso-settings.md),
[profile validation и JWT trust](../repos/auth/internal/modules/sso_settings.go),
[discovery](../repos/auth/internal/modules/sso_discovery.go),
[browser callback/session](../repos/gateway/internal/gateway/browser_sso.go),
[principal policy](../repos/auth/internal/modules/jwt_principals.go),
[directory authorization](../repos/gateway/internal/gateway/identity_directory.go),
[cache isolation](../repos/gateway/internal/provider/cache.go),
[resource ownership](../repos/gateway/internal/gateway/files.go) и
[SSO UI](../repos/gateway/ui/src/pages/SSOSettingsPage.tsx).

### Порядок реализации

1. **P1: разделить browser SSO и API JWT trust.** Отдельные настройки client
   audience и resource audience. Проверять signature, issuer, audience, expiry,
   nonce и привязку authorization code к PKCE flow; ID token не принимать как
   API bearer access token. Сессия хранит bounded internal identity/session ID,
   а актуальные grants берутся из directory. Нечитаемая конфигурация, неизвестный
   issuer и недоступный источник актуальных прав не дают расширенного доступа.
   Миграция сохраняет существующий API trust явно, без скрытой смены issuer.
2. **P1 в выбранном multi-organization этапе: определить tenant boundary.**
   Использовать существующие Organization IDs. Проверять server-side membership
   при каждом выборе org/team; email domain, заголовок или пользовательский body
   не назначают tenant и права. Сначала составить endpoint/permission matrix для
   providers/credentials, models, keys, directory, logs/usage/export, budgets,
   files/vector stores/async resources. Определить ownership migration и доступ
   к старым ресурсам до включения переключения контекста.
3. **P2: несколько SSO connections и API issuers по реальной потребности.**
   Connection имеет ID, provider type, organization binding, точный issuer,
   client ID, endpoints, mappings и собственный lifecycle. API issuer registry
   имеет отдельные audiences и правила claims. Unknown issuer отвергается;
   неподтверждённый `iss` нельзя превращать в URL для сетевого discovery.
   Entra trust перечисляет конкретные разрешённые directories. JWKS кеши
   разделяются по issuer, а не только по `kid`; cardinality и memory bounded.
   PostgreSQL CAS/test/activate/disable/rollback выполняются на уровне connection.
   Шифрование использует `CREDENTIAL_ENCRYPTION_KEY` с domain separation и
   привязкой к connection/organization. Отключение browser connection и отзыв
   API issuer — разные явные действия.
4. **P2: org admin и понятный UI.** Делегированному администратору доступны только
   разрешённые операции своей organization. Сначала внедрить presets Microsoft
   Entra, Keycloak и Generic OIDC с copyable callback URLs. Добавление других
   presets требует подтверждённого callback/token контракта. Показать таблицу
   `Name / Provider / Organization / Issuer / Status / Last test` при нескольких
   connections. Внутри формы: connection, mappings, проверка, activation; active
   и draft различимы, JSON остаётся advanced editor. Mapping rows показывают
   внешний claim/group и внутреннюю роль, без автоматической выдачи admin.
   Диагностика после проверенного login показывает только минимальные
   issuer/subject/audience и результат сопоставления, без JWT/secret payloads.
   Она помогает создать binding, но не заменяет обязательный same-admin proof
   для activation. Login и context selector показывают только допустимые
   варианты; callback state связывает connection, organization и revision.

Порядок зависимостей: разделение trust → tenant/permission contract → connections
и org admin → расширение UI. UI presets и визуальные mappings можно поставлять
раньше, если они честно отражают ограничения существующего runtime.

Сохранить существующие защиты: write-only secrets, общий configuration encryption
key, CAS, same-admin proof с повторной directory проверкой, durable audit,
independent admin virtual-key recovery и fail-closed. JIT provisioning с
одобрением доступа, SAML, external directory group lookup, refresh sessions и
IdP logout — отдельные P3 по бизнес-потребности. Автоматическое объединение
accounts по email и permissive fallback к другому issuer не входят в план.

### Обязательные критерии проверки

- Изменение/disable/rollback browser connection не меняет API trust. Opaque
  access token не мешает browser login с корректным ID token; некорректные
  signature/nonce/audience/issuer и replay отвергаются. Разные доверенные hosts
  проходят endpoint policy; произвольные URL и redirects её не обходят.
- Два issuers с одинаковыми `sub` и `kid`, две organizations и пользователь
  с несколькими memberships сохраняют правильную identity. Нельзя выбрать
  чужую organization/team, подделать claim/header context или повысить роль.
- Cross-organization проверки покрывают credentials/models/keys, cache,
  resources/jobs, request logs/Usage/CSV и budget/spend attribution. Существующий
  ownership не переходит другому tenant при migration или смене контекста.
- Session revocation/logout, deprovisioning, directory/JWKS/DB outage и
  concurrent connection edits проверены regression tests. Sessions, connections,
  JWKS и lifecycle state имеют лимиты и определённую failure/eviction policy.
- PostgreSQL integration на реальной test DB проверяет migrations, CAS,
  cross-replica lifecycle и policy enforcement в CI. Изменения concurrency
  проходят race detector. UI tests покрывают errors, stale async responses,
  mapping validation, keyboard lifecycle и отсутствие секретов.
- Два тестовых OIDC realms проверяют end-to-end federation. Native Entra
  совместимость объявляется только после отдельной проверки реального tenant;
  deterministic unit tests не зависят от IdP, интернета и пользовательских keys.

В этой оценке временные Go overlay audit tests воспроизвели split-origin
rejection, смену API trust managed SSO-профилем и текущий owner scope. Они вместе
с выбранными существующими SSO/JWT/browser regressions успешно выполнены через
`go test -race -count=1` в Auth и Gateway. Временные файлы не включены в репозиторий.
Новые постоянные regression tests обязательны в каждом implementation commit.
Реальный multi-IdP login и PostgreSQL integration для предлагаемых изменений
не выполнялись: сами изменения пока не реализованы. Runtime, UI, deployment,
credentials и правила доступа этой оценкой не изменены.

Дополнительно выполнены `gofmt -w .`, `go vet ./...`, `go test ./...` и
`go build ./...` во всех шести Go modules на Go 1.25.13; все команды успешны,
форматирование не изменило Go-код. Локальные ссылки нового раздела и
`git diff --check` проверены. Полный race suite всех modules, UI component/browser
tests и container build в этой документальной оценке не запускались;
race detector применён к перечисленным Auth/Gateway audit и regression tests.
