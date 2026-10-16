# Пользовательские интерфейсы: LibreChat и Open WebUI

Дата исследования: 2026-10-15. Основной пользовательский chat UI — **LibreChat**.
Open WebUI остаётся поддерживаемым дополнительным приложением. Встроенная
консоль Gateway сохраняет назначение: управление, наблюдаемость и Playground.

LibreChat — отдельное приложение с Node.js backend, MongoDB, авторизацией,
историей, agents и файлами. Это не статический frontend для Go backend.
Разворачивать LibreChat внутри процесса Gateway или переносить его MongoDB
в Gateway PostgreSQL не требуется.

## Границы ответственности

| Область | LibreChat / Open WebUI | AI Gateway |
| --- | --- | --- |
| Вход | Собственный OIDC client и session lifecycle | Проверка исходного access token: подпись, issuer, audience, expiry; directory binding |
| Модели | Выбор доступного публичного model ID | Каталог, актуальные grants, provider/deployment selection, fallback и compatibility |
| Права | Доступ к chat history, agents и локальным файлам приложения | Organization/team/user scopes, model/tool grants, access groups и resource ownership API |
| Лимиты и стоимость | Отображение состояния и ошибок | RPM/TPM, budget reservations, actual usage, billing и audit |
| Чувствительные данные | Хранение истории/вложений и их собственный access control | DLP/AV и анонимизация исходящего модельного контекста по effective policy |
| Agents | Цикл агента, история, пользовательские инструменты | Авторизация каждого модельного запроса и объявленных tools; модель не получает обхода policy |
| Провайдеры | Один endpoint Gateway | Azure OpenAI/Foundry, Ollama, vLLM/SGLang через OpenAI-compatible adapters и другие настроенные deployments |
| Лицензирование | Оператор соблюдает лицензию самого UI | Права на использование моделей задаются grants/access groups; UI не выдаёт entitlements |

Отдельного универсального license server для моделей этот профиль не добавляет.
Model grants не заменяют условия провайдера или лицензию модели. При необходимости
платных entitlements их источник должен обновлять Gateway policy, а не UI catalog.

PII masking в Gateway защищает отправку провайдеру. Исходный prompt уже может
храниться в MongoDB LibreChat, а восстановленный ответ возвращается пользователю.
Поэтому access control, retention, backups и шифрование хранилищ самого UI
настраиваются отдельно; не обещайте анонимизированную историю на основании DLP
Gateway. Аналогично Gateway не может авторизовать сторонний HTTP/tool вызов,
который agent выполняет напрямую: tools/MCP требуют Gateway runtime либо
отдельного согласованного enforcement и egress policy.

## Доверенный пользовательский контекст

Основной поток:

`User → LibreChat → Keycloak access token → Gateway → выбранный deployment`.

1. Импортируйте [realm profile](../examples/identity/keycloak-realm.json).
   `librechat`, `openwebui` и `gateway-console` — разные confidential clients,
   разные client secrets и точные HTTPS callback URLs. Authorization Code + PKCE
   S256; password grant и service accounts у browser clients отключены.
2. Создайте секрет client `librechat` в IdP; в committed realm секретов нет.
   Audience mapper добавляет `gateway` в access token, но не в ID token.
3. Provision directory user, memberships и JWT principal binding в Gateway по
   проверенным `issuer/sub/audience`, как описано в
   [профиле identity](identity-keycloak-openwebui.md#provisioning-и-группы).
   Внутренний ID не обязан совпадать с MongoDB user ID. Email не связывает
   identity. Вход в LibreChat сам по себе не создаёт inference grant.
4. LibreChat передаёт `Authorization: Bearer {{LIBRECHAT_OPENID_ACCESS_TOKEN}}`.
   `OPENID_REUSE_TOKENS=true` включает необходимый session/refresh flow.
   Профиль Keycloak использует `openid profile email`: обычный code flow
   возвращает refresh token без отдельного offline session grant. Для другого
   IdP необходимые scopes и consent проверяются отдельно; добавление
   `offline_access` требует согласованной политики IdP, а не расширения Gateway roles.
   Gateway принимает исходный access token, а не локальный LibreChat JWT или
   OIDC ID token. `X-User-ID`, request `user`, email и metadata не дают полномочий.
5. Ошибка refresh требует повторного входа; shared privileged key для fallback
   отсутствует. В custom endpoint schema поле `apiKey` обязательно: заведомо
   нерабочее `disabled-no-shared-gateway-key` только заполняет этот контракт.
   Никогда не создавайте такой key в Gateway и не заменяйте его admin key.

Вызов запрещённой модели отклоняется Gateway, даже если модель осталась в UI
cache, preset или сохранённом agent. Отзыв directory binding/user действует на
новые запросы независимо от UI session. Logout в UI сам по себе не отзывает все
выданные IdP access tokens: нужны короткий TTL и deprovision события. Title,
summary, agent steps и embeddings — отдельные executions того же пользователя,
с общими лимитами и billing identity после refresh.

## Профиль LibreChat

[Конфигурация](../examples/librechat/librechat.yaml) и
[окружение](../examples/librechat/.env.example) подготовлены для LibreChat
`v0.8.8`, source revision
`e8f3be08623663d4ad7f7241e693c94469b63bb0`. Образ закреплён по manifest digest
в [отдельном Compose profile](../examples/librechat/compose.yaml).
`librechat.yaml` записан в JSON-синтаксисе — допустимом подмножестве YAML 1.2;
это позволяет проверять contract без дополнительной Python зависимости.

Перед запуском замените `models.default: ["approved-model"]` на реальный
operator-approved model ID Gateway, разрешённый целевым пользователям. Upstream
schema требует непустой default; при отказе model fetch UI может показать этот
fallback. Он не выдаёт доступ: Gateway всё равно проверяет текущие grants.
Не добавляйте в общий fallback приватные model IDs; полную изоляцию каталога
нужно подтвердить тестом двух пользователей.

Профиль включает:

- один custom endpoint `AI Gateway`; backend provider credentials отсутствуют;
- `models.fetch=true`, исходный access token для
  `/v1/models` и inference; user-aware cache в закреплённом upstream исходнике;
- title через текущую разрешённую модель, тот же endpoint и пользователя;
- SDK `maxRetries=0`, чтобы 429/ошибки Gateway не скрывались за дополнительными
  клиентскими повторами; retry/failover policy остаётся на стороне Gateway.
  Валидатор профиля проверяет, что опция не передаётся как поле inference API;
- отключённые balance/transactions LibreChat, чтобы они не становились вторым
  финансовым ledger; локальные request/upload safety limits не заменяют Gateway quotas;
- пользовательские agents без public sharing по умолчанию;
- ограничение вложений: до пяти файлов, до 8 MiB суммарно; extracted text
  дополнительно ограничен 200000 символов до проверки контекста в Gateway.

Скопируйте `.env.example` во **внешний защищённый файл** с правами `0600`, замените
все `REQUIRED_*`, настройте authenticated MongoDB, persistent storage и HTTPS
reverse proxy. LibreChat использует собственные `CREDS_KEY`, `CREDS_IV`, session
и OAuth secrets: не переиспользуйте межсервисные секреты Gateway. Все replica
LibreChat должны получать согласованные secrets и общее session/storage состояние.
Сначала создайте отдельного UI operator, затем допускайте обычных пользователей:
первый пользователь новой БД может получить роль администратора LibreChat.
Эта роль не назначает Gateway role/grants; UI operator без Gateway binding
не должен получать inference-доступ. Сквозной тест проверяет это разделение.

После согласования установки образов:

```bash
export LIBRECHAT_ENV_FILE=/secure/path/librechat.env
docker --context rancher-desktop compose -f examples/librechat/compose.yaml config --quiet
docker --context rancher-desktop compose -f examples/librechat/compose.yaml up -d
```

В local profile порт доступен только на loopback; production ingress и
сертификаты остаются обязанностью оператора. Compose не устанавливает IdP,
MongoDB или Gateway и не изменяет существующий deployment. Сначала проверьте
разрешения volumes для UID/GID `1000:1000`. Рабочий URI Gateway должен быть
доступен **из backend LibreChat**, а не только из браузера пользователя.

## Файлы, agents и дополнительные сервисы

| Сценарий | Контракт и условие включения |
| --- | --- |
| Chat и история | UI владеет conversations/messages; в Gateway уходит модельный контекст с identity пользователя |
| Native tool calling | Каждый agent step идёт через Gateway; объявленные tool names должны быть явно разрешены binding; provider/deployment поддерживает tools |
| Изображение | UI передаёт image content; Gateway проверяет vision capability и content policy выбранного маршрута |
| Текстовый файл | LibreChat может извлечь текст и включить его в prompt; Gateway проверяет уже извлечённый текст, лимиты и токены |
| PDF как native file | Проверить фактическую форму payload закреплённого UI; Gateway требует file-input capability либо настроенную обработку Docling. UI upload не означает native capability модели |
| OCR/Docling | Основная обработка документов задаётся на deployment Gateway; сторонний OCR в LibreChat не включается автоматически |
| RAG/embeddings | Не включены в baseline: отдельный RAG service не должен использовать общий privileged key или напрямую ходить к провайдеру. До включения доказать передачу caller identity, grants и per-user billing embeddings |
| Code interpreter, web search, внешние tools/MCP | Не подключены автоматически. Проверить egress/ACL и передачу минимального контекста; локальные side effects не контролируются модельным API Gateway |

Локальные file IDs LibreChat не являются Gateway `/v1/files` IDs. Не подменяйте
одни другими. ACL MongoDB/upload/download проверяет LibreChat; Gateway files,
jobs и conversations имеют самостоятельную owner boundary. Между двумя UI
автоматического переноса истории, agents или файлов нет. Перенос выполняется
отдельно с подтверждённым ownership, без общей папки uploads.

## Совместимость и критерии приёмки

Состояние на 2026-10-15: профиль проверен настоящей configuration schema и SDK
закреплённого LibreChat. В изолированном Rancher Desktop стеке успешно выполнены
real-service сценарии **LibreChat v0.8.8 + MongoDB 8.0.20 + Keycloak 26.3.3**
и сохранённый сценарий **Open WebUI v0.11.4** с реальной тестовой PostgreSQL.
Рабочий deployment не менялся. Upstream model responses в тестах детерминированные:
облачные credentials, реальные модели и GPU deployments этим запуском не проверены.

Проверены browser OAuth/PKCE, ID-token rejection, раздельный каталог двух пользователей,
чередующиеся/параллельные запросы, полный SSE и история, текстовые upload/download
ACL, ownership сохранённых agents и отзыв model grants. Подтверждены PII masking
до модельного провайдера, DLP deny и fail-closed при недоступном обязательном DLP,
раздельный PostgreSQL billing, отсутствие prompt/document content в billing outbox,
видимые ошибки квот, refresh и deprovision активной UI session без shared-key fallback.

Перед переводом основной ссылки пользователей на LibreChat требуется:

1. Реальный вход двух пользователей через Keycloak Authorization Code + PKCE,
   разные `/api/models`, чередующиеся и параллельные запросы без утечки cache.
2. Полный UI chat endpoint с SSE, сохранение и повторное чтение истории;
   отсутствие доступа к conversation/file другого пользователя.
3. Прямой вызов скрытой модели, spoofed user headers, локальный UI token,
   непровиженный user и отключённый directory user не дают inference-доступ.
4. Refresh не меняет attribution, общий RPM/budget или resource ownership;
   отказ IdP и повторный вход не включают shared-key fallback.
5. Gateway billing в реальной тестовой PostgreSQL учитывает обоих пользователей
   раздельно; 429 и отказ бюджета видны в UI без успешного provider execution.
6. Tool roundtrip и attachment matrix проверены на нужных бизнесу моделях.
   Любой неподтверждённый режим остаётся явно выключенным до приёмки.
7. CI выполняет отдельные проверки LibreChat и сохранённый Open WebUI сценарий.
   Runtime tokens, passwords и raw OAuth logs не публикуются как artifacts.

Offline проверка: `python3 -B scripts/test_identity_profile.py`.
Воспроизводимый real-service сценарий:
`IDENTITY_INTEGRATION_TESTS=true python3 -B scripts/test-librechat-integration.py`.
Он использует те же три выделенные тестовые PostgreSQL DSN, проверяет наличие
закреплённых образов до изменения БД и не скачивает их автоматически. Keycloak
работает через disposable HTTPS proxy с временным доверенным CA; проверка
сертификатов и hostname остаётся включённой в browser OAuth и Node/Python clients.
Linux Go проверяет JWKS по HTTPS с тем же CA. На macOS Go использует Keychain,
поэтому только публичные тестовые JWKS доступны Auth через loopback HTTP;
issuer остаётся HTTPS, проверяются подпись/audience/expiry. Тест не меняет Keychain
и не отключает TLS verification. Этот локальный обход не является production настройкой.
Test client учитывает resumable protocol закреплённого LibreChat: POST запускает
generation, затем отдельный GET читает SSE с `generationCreatedAt` fence.
Успех требует terminal response message без error/unfinished, а не только
HTTP 200 или текстового delta. Негативные запросы проверяются по уникальным
синтетическим markers, чтобы фоновые title executions не искажали результат.
Сам HTTPS fixture независимо проверен без Docker:
`python3 -B -m unittest scripts/test_identity_tls_proxy.py` покрывает доверенный CA,
отказ недоверенного CA, hostname mismatch и завершение процесса по SIGTERM.
CI matrix содержит отдельные `librechat` и `openwebui` проверки. Успешный локальный
запуск не заменяет результат CI и приёмку с реальным IdP/моделями целевой среды.
Существующий Open WebUI сценарий:
`IDENTITY_INTEGRATION_TESTS=true python3 -B scripts/test-identity-integration.py`
с тремя выделенными тестовыми PostgreSQL DSN из
[руководства identity](identity-keycloak-openwebui.md#воспроизводимая-проверка).
Изменение primary UI не требует изменения публичного Gateway API, provider
adapters, grants, budgets или формата credential.

## Сохранение Open WebUI и переход

[Профиль Open WebUI](identity-keycloak-openwebui.md) и его integration job
сохраняются. Общими остаются Gateway directory, policies и billing; OIDC
clients и собственные базы UI разделены. Сначала подключите LibreChat к тем же
согласованным Gateway model IDs, provision пользователей, выполните acceptance
matrix, затем измените основную ссылку на chat UI. Откат — вернуть прежнюю
ссылку; он не требует отката Auth/Gateway migrations или переноса данных UI.

У закреплённого LibreChat лицензия MIT. Сохраняйте notices при распространении
и проверяйте лицензии его отдельных optional services. Лицензия Open WebUI
проверяется для фактически используемой версии; её ограничения не меняются
от переключения основной ссылки на LibreChat.

Источники: [LibreChat custom endpoints](https://www.librechat.ai/docs/configuration/librechat_yaml/object_structure/custom_endpoint),
[OIDC token reuse](https://www.librechat.ai/docs/configuration/authentication/OAuth2-OIDC/token-reuse),
[upstream v0.8.8](https://github.com/LibreChat-AI/LibreChat/tree/e8f3be08623663d4ad7f7241e693c94469b63bb0),
[model fetch](https://github.com/LibreChat-AI/LibreChat/blob/e8f3be08623663d4ad7f7241e693c94469b63bb0/packages/api/src/endpoints/models.ts),
[header resolution](https://github.com/LibreChat-AI/LibreChat/blob/e8f3be08623663d4ad7f7241e693c94469b63bb0/packages/api/src/utils/env.ts).
