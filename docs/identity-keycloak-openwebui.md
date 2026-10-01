# Keycloak и пользовательский OpenWebUI

Этот профиль использует предварительное provisioning, отдельные OIDC clients
и исходный OAuth access token. Gateway самостоятельно назначает права directory.
Не включайте `directory` для рабочего Auth до provisioning и обновления Gateway.

## Realm и доверие

[Пример realm](../examples/identity/keycloak-realm.json) не содержит users,
паролей или client secrets. Замените `identity.example.com`, `chat.example.com`
и `gateway.example.com` на согласованные HTTPS endpoints. После импорта создайте
секреты confidential clients в Keycloak и передайте их через используемый secret
store. Для browser clients включён Authorization Code + PKCE; password grant и
service accounts отключены. `openwebui` и `gateway-console` — разные clients.

Audience mapper добавляет `gateway` только в access token. Gateway проверяет
issuer realm, `aud=gateway`, подпись и expiry. Не используйте ID token или
локальный OpenWebUI session JWT для inference. Client role mapper публикует
`resource_access.gateway.roles`; role scope mapping не назначает роли users.
Оператор отдельно назначает каждому пользователю необходимую client role.

[Auth values](../examples/identity/auth-keycloak.values.yaml) задают явную таблицу
`gateway-user → user`, `gateway-admin → admin`, `gateway-team-admin → team_admin`.
Соответствующая внутренняя роль также должна быть назначена directory user;
одного claim недостаточно для административного доступа. Хеширование keys и
management channel требуют отдельных секретов, одинаковых у нужных сервисов.
Файл values — overlay доверия, а не самостоятельная production-конфигурация БД
или secrets. Не оставляйте chart dev secrets в рабочем окружении.

## Provisioning и группы

1. Создайте active user через `PUT /admin/v1/users/{internal-id}` либо SCIM Users.
   Используйте внутренний ID, а не email. Назначьте минимальные directory roles.
2. При необходимости создайте team и membership, привяжите team к organization.
   Group-to-team mapping выполняется оператором или выбранным provisioning
   сервисом через directory/SCIM API. Group claim не создаёт membership.
3. Создайте `PUT /admin/v1/jwt-principals` с body:

   ```json
   {
     "issuer": "https://identity.example.com/realms/gateway-users",
     "subject": "verified-keycloak-user-subject",
     "audience": "gateway",
     "user_id": "directory-user-id",
     "team_id": "directory-team-id",
     "enabled": true,
     "allowed_models": ["approved-model"],
     "allowed_tools": ["read"],
     "access_group_ids": [],
     "tags": [],
     "rate_limit_rpm": 60,
     "rate_limit_tpm": 100000
   }
   ```

4. Проверяйте bindings через `GET /admin/v1/jwt-principals?user_id=...`.
   Только global admin может управлять этими привязками; mutations требуют audit.
   Subject берётся из проверенного access token/Keycloak user ID, не из email.
   Дублирование email не связывает аккаунты. Для другой audience создаётся
   отдельный credential scope; внутренний user budget остаётся общим.
5. При отключении пользователя в IdP синхронно отключите directory user или
   binding. Встроенной отправки SCIM из Keycloak этот профиль не предполагает.
   Источник provisioning и deprovision событий должен быть выбран оператором.

Неизвестные principals, disabled/deleted users, отсутствующее membership или
неактивная organization получают отказ. Пустые model/tool grants означают
запрет; `*` — явное разрешение всего. Нулевые RPM/TPM означают отсутствие лимита.
Refresh access token сохраняет внутренний user и credential ID. Existing virtual
keys сохраняют прежнюю семантику пустых grants. Обновление grants/membership
меняет cache scope; directory проверяется при каждом новом запросе.

## OpenWebUI connection

Проверенный профиль: Keycloak `26.3.3`, OpenWebUI `0.11.4` (образы
зафиксированы по digest в integration tests). OpenWebUI `0.8.12` не создаёт
OAuth session cookie, а `0.9.0` использует общий ключ model-list cache; эти
версии не подходят для данного профиля. Более ранние и последующие версии
требуют такой же проверки двух пользователей.

Настройте OIDC в OpenWebUI с `OAUTH_CLIENT_ID=openwebui`, отдельным client secret,
`OPENID_PROVIDER_URL=https://identity.example.com/realms/gateway-users/.well-known/openid-configuration`,
`OAUTH_SCOPES=openid email profile` и `OAUTH_CODE_CHALLENGE_METHOD=S256`.
Задайте `OAUTH_SESSION_TOKEN_ENCRYPTION_KEY` (Fernet key) и стабильный
`WEBUI_SECRET_KEY` через secret store; сохраняйте их между restart/replicas;
refresh tokens не должны попадать в browser JavaScript, логи или Git.

В Admin Settings → Connections добавьте OpenAI-compatible URL
`https://gateway.example.com/v1` и OAuth auth mode **System OAuth**
(`auth_type=system_oauth`). Shared API key в этой connection не нужен.
`session` передаёт локальный OpenWebUI JWT, а `bearer` использует connection key;
эти режимы не обеспечивают выбранный per-user flow. Ошибка/отсутствие OAuth
session не должна переключать connection на privileged shared key.

Сохраняйте `ENABLE_BASE_MODELS_CACHE=false`: общий base-model cache внешнего
интерфейса не должен подменять пользовательские каталоги. Для проверенного
обычного chat-сценария отключён evaluation arena; в Controls → Function Calling
выбран **Legacy**, чтобы OpenWebUI не добавлял built-in tools автоматически.
Для Native режима явно назначьте нужные имена tools в Gateway binding или
отключите built-in tools в model capabilities OpenWebUI. Gateway отклоняет весь
запрос, если в нём есть неназначенный tool; не расширяйте grants до `*` ради входа.

Gateway не принимает `X-OpenWebUI-*`, request `user` или metadata как identity.
Проверяйте `/v1/models` для каждого пользователя и прямой вызов скрытой модели.
Даже устаревший внешний model list не разрешает вызов запрещённой модели.
Для attachments действуют authenticated file ownership и model grants; для RAG
нужны отдельные явно назначенные embedding/generation models.

Фоновые title/summary запросы сохраняют пользовательскую OAuth session и
учитываются как его executions. Unattended tasks требуют отдельного ограниченного
service principal/key; их расход нельзя приписывать человеку по заголовку.

## Background, отзыв и переход

Queued directory JWT batch повторно проверяет principal и policy перед execution.
Изменение directory policy прекращает такой item; ошибка directory оставляет его
на retry. Access groups, tags, fallback grants и policy attachments проверяются
заново. Уже запущенные Responses/Interactions при подтверждённом отзыве получают
cancel; settlement ждёт terminal retrieval с usage и сохраняет исходную attribution.
Не освобождайте reservation только по acknowledgment отмены. Completed work
учитывается даже после отключения владельца; idempotency использует execution ID.

JWKS validation не выполняет introspection при каждом запросе. После logout в
Keycloak уже выданный access token остаётся пригодным до expiry, если directory
не отключён. Профиль использует access token TTL 300 секунд; для более быстрого
отзыва требуется событие deprovision или отдельно согласованный introspection
flow. Browser console logout очищает локальную Gateway cookie; RP/back-channel
logout и расширенные server sessions относятся к поставке E roadmap. Текущий
console token limit — 2800 байт, cookie не refresh-ится; сократите mapper claims
для console или выберите поставку E. Не увеличивайте cookie без серверных sessions.

Сначала примените additive migration `013_jwt_principals.sql` и совместимый
Gateway, затем provision bindings и включите `AUTH_JWT_IDENTITY_MODE=directory`.
Legacy fingerprint resources не переносятся автоматически: подтвердите owner
для каждого migration либо оставьте legacy доступ до окончания retention.
Rollback — вернуть `legacy` и исходные IdP settings. Stable-ID ресурсы остаются
в своём namespace и не должны становиться доступными legacy fingerprint owner.

Контракты: [Keycloak OIDC](https://www.keycloak.org/securing-apps/oidc-layers),
[OpenWebUI SSO](https://docs.openwebui.com/features/authentication-access/auth/sso/),
[OpenWebUI Keycloak](https://docs.openwebui.com/features/authentication-access/auth/sso/keycloak/).

## Воспроизводимая проверка

`python3 -B scripts/test_identity_profile.py` проверяет production realm offline.
Для сквозного сценария задайте `IDENTITY_INTEGRATION_TESTS=true` и три отдельные
**тестовые** PostgreSQL DSN: `AUTH_POSTGRES_TEST_DSN`, `BILLING_POSTGRES_TEST_DSN`,
`CONTROL_PLANE_POSTGRES_TEST_DSN`; выполните
`python3 -B scripts/test-identity-integration.py`. Нужны объявленный Go toolchain,
Python, `psql`, Helm и Docker; на macOS используется работающий Rancher Desktop.
Порты Auth/Billing 8082/8083 должны быть свободны. Скрипт создаёт отдельную
Gateway database, realm, контейнеры и runtime secrets; останавливает только свои
процессы и контейнеры. Переданные БД изменяются, поэтому рабочие DSN запрещены.

Проверяются настоящий Authorization Code + PKCE и server OAuth refresh,
раздельный model-list cache, JSON/SSE, отсутствие fallback без OAuth session,
401/429, grants/tools/tool results, files/Conversations ownership, embeddings,
12 параллельных запросов, shared quota/budget после refresh, SQL attribution,
expiry, rotation/logout, отказ directory SQL и восстановление IdP/JWKS.
Provider — детерминированный локальный HTTP fixture, поэтому результат не
доказывает совместимость конкретной внешней модели или полного RAG pipeline.
Тест HTTP/loopback settings не переносятся в production realm.

CI выполняет этот сценарий отдельно от обязательных PostgreSQL regression tests
для reservations, background cancellation/settlement и повторной доставки
billing events. Runtime OAuth tokens, passwords и raw logs не публикуются как
CI artifacts. Внешний UI и IdP не входят в Gateway deployment; их рабочие
адреса, secrets и источник provisioning настраиваются оператором.
