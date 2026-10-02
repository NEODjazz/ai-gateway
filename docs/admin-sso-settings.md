# Настройка browser SSO в UI

Администратор открывает **System → Settings → Single sign-on**. Browser login
использует OIDC authorization code с PKCE S256 и nonce. Managed SSO проверяет
RS256/ES256 ID token для browser client, независимо от resource access tokens API. Service principal для
доступа к Azure inference настраивается отдельно в Providers / Credentials.

## Подготовка

- Auth использует PostgreSQL; сначала примените additive migration
  `repos/auth/migrations/postgres/014_sso_settings.sql` и
  `repos/auth/migrations/postgres/015_sso_sessions.sql`, затем обновляйте Auth.
  Миграции также включены в `charts/postgres` ConfigMap **data**.
- `CREDENTIAL_ENCRYPTION_KEY` должен содержать минимум 32 байта. Gateway и все
  реплики Auth используют одинаковое значение для шифрования конфигурации.
  Ключи AES-GCM для SSO, MCP, logging и A2A выводятся с отдельными domain separators;
  существующий формат provider credentials сохранён.
  `AUTH_KEY_HASH_SECRET` остаётся независимым ключом хеширования virtual keys;
  его нельзя менять при переименовании encryption key.
- Gateway имеет настроенные Auth management URL/shared secret и durable audit.
  Internal management endpoints не должны публиковаться через ingress.
- Сохраните рабочий virtual key с ролью `admin`, принадлежащий внутреннему
  пользователю, который будет проверять SSO. Активация, отключение и rollback
  доступны только из такой key session; SSO session не может менять доверие IdP.

В обоих Helm releases укажите один существующий Secret в том же namespace:

```yaml
credentialEncryption:
  existingSecret: ai-gateway-config-encryption
  secretKey: CREDENTIAL_ENCRYPTION_KEY
```

Secret создаётся отдельно через ваш механизм управления секретами. Для переноса
существующего provider Secret можно временно указать его прежнее имя поля в
`secretKey`: значение ключа остаётся тем же, а переменная процесса уже называется
`CREDENTIAL_ENCRYPTION_KEY`. Inline `credentialEncryption.key` предназначен только
для зашифрованного values source; его нельзя совмещать с `existingSecret`.

## Совместимость ключей

Старое имя `PROVIDER_CREDENTIAL_ENCRYPTION_KEY` принимается как deprecated alias
при отсутствии нового. Два разных значения запрещены: процесс не должен молча
переключить ключ уже сохранённых данных. Простое переименование с тем же значением
сохраняет provider credentials, MCP, logging и A2A без перешифрования.

SSO-документ предыдущей версии мог быть зашифрован через `AUTH_KEY_HASH_SECRET`.
После задания общего ключа Auth читает такой документ старым ключом и автоматически
перешифровывает с revision CAS. Active/previous/draft, proof, profile IDs и session
keys сохраняются; concurrent edit перечитывается, а не перезаписывается. Сохраните
прежний hash secret для этого перехода. До задания общего ключа старый SSO-профиль
можно читать, но нельзя записывать новый; UI настройки возвращает unavailable.
Нечитаемый существующий документ закрывает JWT/SSO вход без fallback на env trust.
Пустая таблица сохраняет прежние environment JWT настройки.

Это перенос со старого источника SSO-ключа, а не механизм произвольной ротации
общего ключа. Его замена новым значением требует отдельного переноса всех
зашифрованных конфигураций. Уже обновлённые данные старые версии Auth не прочитают:
обновляйте все реплики согласованно и сохраняйте резервную копию БД.

## Конфигурация

1. Введите точный issuer. **Discover endpoints** загружает authorization endpoint,
   token endpoint и JWKS. Discovery не выполняет вход и не активирует профиль.
   Endpoints должны иметь origin issuer либо быть явно перечислены в
   **Trusted additional endpoint origins** (не более восьми origins без paths).
   Discovery не добавляет разрешения автоматически; redirects при token/JWKS
   загрузке не выполняются. Используйте HTTPS; HTTP
   outbound допускается только для `localhost`, `127.0.0.1` и `::1` в тестах.
2. Укажите browser client ID и scopes, включая `openid`. Audience ID token и
   directory binding автоматически равен client ID; resource audience API
   настраивается независимо. Для Entra используйте конкретный tenant-specific
   v2 issuer и app roles browser-приложения в ID token. Для Keycloak mapper ролей
   browser client должен иметь `id.token.claim: true` (пример в
   `examples/identity/keycloak-realm.json`). Opaque access token допустим:
   он не является browser identity и не сохраняется в сессии. Если ID token
   содержит `at_hash`, он сверяется с полученным access token.
3. Укажите roles claim path и явную карту внешних ролей в `user`, `developer`,
   `team_admin`, `admin`. Для Keycloak профиль использует
   `resource_access.gateway.roles`; для Entra app roles обычно `roles`.
   Роли дополнительно пересекаются с текущими approved roles пользователя в Auth.
4. Callback должен оканчиваться `/auth/sso/callback`. Зарегистрируйте у IdP **оба**
   показанных URI: основной и `/auth/sso/test/callback`. Для локального ingress
   разрешён HTTP redirect в `*.localhost`; production требует HTTPS.
5. Введите client secret для confidential client. Secret write-only: пустое поле
   сохраняет прежний secret; checkbox **Clear saved client secret** удаляет его.
   При смене issuer/client ID нужно явно заменить или очистить secret. Secret,
   session key и тестовые JWT не попадают в публичные ответы или audit payloads.
6. **Save draft** сохраняет зашифрованный черновик с revision CAS. Редактирование
   сбрасывает proof и меняет session key черновика. Активный профиль не меняется.

## Пользователи и проверка

В **Users** предварительно создайте active пользователя и назначьте approved
roles. Форма **Bind identity to a Gateway user** привязывает точные
`issuer + sub + browser client ID` к internal user ID. Email не используется для привязки,
автоматического создания пользователей и назначения ролей нет. Ownership
существующего subject неизменяем. Пустые allowed models/tools запрещают inference
и tools; для доступа к моделям выдайте явные grants. Изменение существующей
привязки заменяет переданные grants, поэтому проверяйте её policy перед сохранением.

Нажмите **Test sign-in**, войдите в открывшейся вкладке как тот же internal user,
которому принадлежит текущий admin key, и вернитесь к **Refresh test status**.
Проверка имеет срок пять минут и требует актуальную роль `admin`, подписанный
ID token, nonce, точные issuer/client audience и действующую directory binding. Тест не
создаёт активную browser session и не заменяет текущий вход администратора.

После статуса `passed` нажмите **Activate SSO**. При активации Auth повторно
проверяет directory policy: деактивация пользователя или смена прав после теста
отклоняет активацию. Запоздавший proof, повторный callback и stale revision
отклоняются. Оператор после 409 должен перечитать настройку и повторить действие.

## Действие на систему и восстановление

Browser SSO и API JWT trust независимы. Активация, отключение и rollback browser
профиля не меняют issuer, audience и role policy API-клиентов. В текущем API
контракте поддерживается один issuer; multi-issuer federation отсутствует.
Virtual keys сохраняют независимый доступ. При обновлении уже сохранённой managed
конфигурации её прежнее глобальное API trust один раз копируется в отдельную
зашифрованную настройку с revision CAS. Это сохраняет действующих API-клиентов;
последующие изменения browser профиля не меняют эту настройку. Новые установки
используют environment API JWT trust независимо от browser профиля.
Все Auth/Gateway реплики читают общий PostgreSQL document; настройки не требуют
рестарта или правки ConfigMap после подготовки runtime dependencies.

**Disable browser SSO** отключает browser login и cookies, сохраняя JWT trust для
других клиентов. **Roll back** восстанавливает один предыдущий managed profile;
rollback первой активации отключает managed browser login; API trust сохраняется.
Хранится только один предыдущий профиль, а не полная история. При недоступности
Auth/БД или неверном encryption key JWT/cookie вход закрывается. Virtual key не
зависит от SSO profile и позволяет восстановить настройку при исправном Auth;
stored keys также требуют доступной БД. Полный отказ Auth не обходится этим
механизмом. Logout удаляет cookie даже при сбое Auth; если server-side revocation
не удалось, возвращает ошибку вместо подтверждения отзыва.

Без managed active profile browser login отключен; прежние API JWT настройки
продолжают использоваться независимо. Новый профиль хранится AES-GCM encrypted в одной bounded строке
`auth_sso_settings`: active, previous, draft и одна test attempt. JWKS verifier
cache для отдельного API trust хранит один verifier на реплику. Browser
ID-token verifier используется во время login; сессии не требуют JWKS при
каждом запросе.

Managed сессия использует encrypted HttpOnly cookie с случайным opaque handle.
В PostgreSQL сохраняются только hash handle и зашифрованная минимальная identity;
ID/access/refresh tokens не сохраняются. Lifetime равен configured TTL (60–86400
секунд) и не зависит от оставшегося времени ID/access token. Directory grants
и approved roles проверяются при каждом запросе. Logout отзывает session во
всех репликах; disable/смена профиля закрывают вход. Rollback меняет session key
и не восстанавливает старые сессии. Background jobs также перепроверяют сессию.

Admission сериализован между репликами: до 10000 сессий суммарно, 1000 на профиль,
16 на пользователя. Истекшие записи удаляются при admission; заполнение активными
сессиями возвращает unavailable. Nonce replay state хранится 10 минут отдельно
от sessions, в пределах 20000 записей, поэтому logout не разрешает повторный
callback. ID token должен быть выдан не ранее пяти минут назад. Старый
environment-only browser flow временно сохраняет прежний access-token контракт;
для нового ID-token/session flow перенесите настройку в managed UI. Существующий
managed профиль нужно пересохранить с client audience и повторить binding/test;
прежняя API binding не заменяется автоматически. SAML, refresh sessions и
автоматический JIT provisioning сюда не входят.

## Проверки

Regression tests покрывают encryption/redaction, CAS и concurrent replicas,
invalid URL/role/scopes, proof replay/expiry, same-admin check, deprovisioning,
API-key recovery, cookie rotation, outage fail-closed и отсутствие session в тесте.
`scripts/test-postgres-integration.sh` выполняет PostgreSQL и race tests.
`scripts/test-identity-integration.py` выполняет реальный Keycloak PKCE flow для
черновика и активного входа, активацию, запрет trust changes из SSO session,
disable и rollback на отдельной тестовой БД; этот сценарий входит в CI.

Контракты IdP: [Microsoft OIDC endpoints](https://learn.microsoft.com/en-us/entra/identity-platform/v2-protocols-oidc),
[Microsoft ID token claims](https://learn.microsoft.com/en-us/entra/identity-platform/id-token-claims-reference).
Реальный Entra tenant в локальных проверках не использовался.

## Multiple browser connections

Apply Auth migration `017_sso_connections.sql` before updating Auth. The existing
singleton is retained as connection `default`, with its encrypted settings,
revision, API trust snapshot and browser sessions preserved. Readiness requires
the connection table. No deployment configuration or credentials are changed by
this migration.

Platform administrators use `GET /admin/v1/sso/connections` and
`POST /admin/v1/sso/connections`. Creation accepts `id` (lowercase letters,
numbers, `_` and `-`, at most 64), `name` (at most 128), `provider` (`entra`,
`keycloak`, `oidc`) and optional `organization_id`. Additional connections are
bounded to 16; creation across replicas is serialized in PostgreSQL. Metadata and
organization binding are immutable. Provider labels select UI guidance, not a
weaker validation or authentication mode.

Existing settings/test/action endpoints accept `?connection=<id>`. An unknown
connection never falls back to default. Each connection has its own encrypted
active/draft/previous document, CAS revision, proof ticket and rollback epoch.
Encryption is bound to the connection's immutable metadata using
`CREDENTIAL_ENCRYPTION_KEY`; copied ciphertext cannot move to another connection
or tenant. Client/session secrets are never returned by the admin API. API JWT
trust is independent and remains in the default trust configuration.

For a bound connection, draft `organization_id` must equal its binding. It cannot
map `admin` or `team_admin`: map the tenant administrator to `org_admin`, which
requires explicit active organization membership. Testing additionally proves
that the same internal user is an approved platform administrator in the directory;
the resulting browser session still contains only mapped, tenant-approved roles.
Changing connection issuer/client never transfers a principal's immutable tenant.
Use separate approved issuer/client bindings for access to multiple organizations.

`/auth/sso/config` lists enabled connection names and start URLs. Selecting
`/auth/sso/start?connection=<id>` starts a fresh PKCE/nonce login. The connection
hint in cookies is only routing information: both the encrypted cookie purpose
and profile are verified before exchanging a code. Auth checks the ID token and
pinned directory organization before issuing a local server session. Public
headers/query labels cannot select a tenant for an existing credential. Existing
API clients continue to present their independent resource tokens.

Unit/browser tests cover tampered routing hints before code exchange, unknown
connections, independent proof/revisions, server sessions and disable behavior.
PostgreSQL tests cover replica CAS, bounded concurrent admission and ciphertext
binding. The UI provides connection management and provider presets.

## Role/group mappings and verified identity metadata

Browser profiles may configure `groups_claim` (a nested claim path) and bounded
`group_mappings` in addition to `roles_claim` / `role_mappings`. Both sources map
only values from the verified ID token. Their role targets are intersected with
current directory/organization approvals; mapping a group never creates a user,
membership or platform permission. Malformed claim values are rejected. Group
claims omitted by an IdP, including group-overage indirections, do not authorize
access through an implicit directory lookup.

Settings expose `last_test_at`, `last_test_status` and `verified_identity`.
Identity metadata contains only issuer, subject, client audience, verification
time and, after approval, internal user/organization and effective roles.
`approved=false` means the ID token was checked but directory/same-admin approval
failed; it cannot activate SSO or create a server session. This allows an operator
to obtain the exact verified subject before saving a principal binding. Invalid
signature, issuer, audience, nonce or token lifetime returns no preview. A new
draft/test clears the old preview. Raw tokens, email/group claims, session handles
and policy digests are never returned.

## Connection management in the console

Open **Settings → Single sign-on**. The connections table displays Name, Provider,
Organization, Issuer, Status and Last test. Select Configure to edit only that
connection. Switching clears unsaved client secrets and ignores older loads.
Add connection accepts an exact existing organization ID or an empty platform
binding; server validation rejects unknown/disabled organizations. Metadata is
immutable, so a new tenant needs its own connection.

Entra presets require a specific tenant GUID and configure the v2 issuer and
`roles` / `groups` claims. Keycloak presets configure the realm issuer and
`realm_access.roles` / `groups` paths. Generic OIDC supports explicit issuer and
claim paths. Apply a preset, discover endpoints, review mappings and save the
draft. Group claims must be configured by the identity provider; omitted or
over-limit group claims do not trigger an implicit directory fetch. Presets do
not grant permissions or bypass endpoint validation.

Visual role/group rows use exact external values and approved Gateway roles.
Incomplete or duplicate rows block saving. Organization-bound editors exclude
platform `admin` and `team_admin`. Advanced JSON remains available. After a test,
the verified identity section distinguishes cryptographic verification from
directory approval. Use verified subject explicitly fills the binding form;
saving a binding still requires an approved directory user and pinned tenant.

The sign-in page lists enabled connections with their organization. Each choice
starts a fresh OIDC login; choosing a connection never grants membership or
changes the organization of an existing API credential. API trust and recovery
virtual keys remain independent.

## Compatibility: legacy browser profile migration

Environment-only `ADMIN_SSO_*` browser authorization is retired. It previously
used API resource access tokens for browser identity and stored them in encrypted
cookies. Those cookies no longer authorize console requests. Environment/API JWT
configuration is retained for API clients; it does not supply browser trust.
Configure an independent managed connection, bind an approved principal, complete
its sign-in test and activate using a platform administrator virtual key.
`/auth/sso/config` exposes `migration_required=true` for an inactive legacy profile.
Rolling back the first activation disables browser sign-in; it never restores
resource-token browser fallback. No secrets, directory records or API keys are
migrated or deleted automatically.

## Independent API issuer registry

Browser connections do not configure API access-token trust. The primary
`AUTH_JWT_*` configuration and any explicitly preserved legacy API trust retain
existing behavior. Platform administrators can additionally manage at most 16
API issuer entries through `/admin/v1/api-issuers`. Each entry pins an immutable
ID, name, Organization, exact issuer and resource audience. Moving to another
identity namespace requires a separate entry and explicit principal approval.
Migration `018_api_issuers.sql` is required when the shared configuration
key enables the registry.

The registry uses separate encrypted PostgreSQL rows, CAS revisions and an
AEAD domain bound to immutable metadata under `CREDENTIAL_ENCRYPTION_KEY`.
Only JWKS signed RS256/ES256 access tokens and directory identity mode are
supported for these entries. The configured JWKS URL must use the issuer origin;
HTTP is allowed only for loopback testing. Role mappings cannot assign platform
administrator roles in an Organization-bound entry. A browser client audience
cannot also be registered as an API audience. Issuers from unverified token
claims select an explicitly configured entry; they never become discovery URLs.
Tokens matching several configured resource audiences are rejected as ambiguous.

Creating or changing API trust requires an administrator virtual key and durable
Gateway audit. PUT `/admin/v1/api-issuers/{id}` saves a draft with
`expected_revision`, `jwks_url`, `roles_claim` and `role_mappings`. POST
`/{id}/test` verifies a bounded resource `token` against that draft and the
operator's current approved principal. The token is write-only and never stored;
only the internal authorization proof is encrypted at rest. An Organization-bound
proof also requires an approved `org_admin` membership for the same platform
operator. The proof expires at the earlier of token expiry and five minutes.
POST `/{id}/action` accepts `activate`, `disable` or `rollback` with the expected
revision; activation rechecks current directory approvals. Disabling an API
issuer also denies reauthorization of its durable jobs, without disabling a
browser connection or another API issuer. Saving a new draft invalidates its
old test proof. A stale revision produces 409; malformed/expired trust produces
400; failed identity approval produces 403; inaccessible configuration/directory
produces 503.

JWKS caches are separated by entry and active profile, with at most 16 registry
verifiers plus the primary verifier. Candidate test verifiers are request-local;
revoked/replaced profiles are evicted when configuration is loaded. Each verifier
retains at most 64 keys and bounds the response to 1 MiB. Refresh network I/O
runs outside its cache lock; concurrent refreshes share one result and canceled
waiters can leave. Additional IdP availability is checked on the relevant API
request rather than polling all IdPs during readiness.
