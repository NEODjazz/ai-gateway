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

Профиль включает:

- один custom endpoint `AI Gateway`; backend provider credentials отсутствуют;
- `models.fetch=true`, пустой fallback catalog, исходный access token для
  `/v1/models` и inference; user-aware cache в закреплённом upstream исходнике;
- title через текущую разрешённую модель, тот же endpoint и пользователя;
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

Состояние на 2026-10-15: изучен закреплённый upstream, подготовлены профиль и
offline contracts. **Сквозной тест реального LibreChat ещё не выполнен**;
скачивание необходимых образов в Rancher Desktop требует согласования по
`AGENTS.md`. До успешного запуска профиль не считается production-validated.

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
Подготовлен отдельный real-service сценарий:
`IDENTITY_INTEGRATION_TESTS=true python3 -B scripts/test-librechat-integration.py`.
Он использует те же три выделенные тестовые PostgreSQL DSN, проверяет наличие
закреплённых образов до изменения БД и не скачивает их автоматически. Keycloak
работает через disposable HTTPS proxy с временным доверенным CA; проверка
сертификатов и hostname остаётся включённой. Этот сценарий пока не запускался.
CI matrix содержит отдельные `librechat` и `openwebui` проверки; наличие job
не означает успешного прохождения ещё не выполненного запуска.
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
