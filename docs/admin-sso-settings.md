# Настройка browser SSO в UI

Администратор открывает **System → Settings → Single sign-on**. Browser login
использует OIDC authorization code с PKCE S256. Поддерживаются IdP с RS256/ES256
access token, например Microsoft Entra ID или Keycloak. Service principal для
доступа к Azure inference настраивается отдельно в Providers / Credentials.

## Подготовка

- Auth использует PostgreSQL; сначала примените additive migration
  `repos/auth/migrations/postgres/014_sso_settings.sql`, затем обновляйте Auth.
  Миграция также включена в `charts/postgres` ConfigMap **data**.
- `AUTH_KEY_HASH_SECRET` должен содержать минимум 32 байта. Из него с отдельным
  domain separator выводится AES-GCM key для SSO secrets. Все реплики Auth должны
  иметь одинаковое значение. Смена этого ключа требует отдельной миграции
  зашифрованного документа; простая замена делает сохранённый профиль нечитаемым.
- Gateway имеет настроенные Auth management URL/shared secret и durable audit.
  Internal management endpoints не должны публиковаться через ingress.
- Сохраните рабочий virtual key с ролью `admin`, принадлежащий внутреннему
  пользователю, который будет проверять SSO. Активация, отключение и rollback
  доступны только из такой key session; SSO session не может менять доверие IdP.

## Конфигурация

1. Введите точный issuer. **Discover endpoints** загружает authorization endpoint,
   token endpoint и JWKS. Discovery не выполняет вход и не активирует профиль.
   Issuer и эти endpoints должны иметь один origin. Используйте HTTPS; HTTP
   outbound допускается только для `localhost`, `127.0.0.1` и `::1` в тестах.
2. Укажите client ID, **audience access token** и scopes, включая `openid`.
   Audience ресурса может отличаться от client ID приложения browser login.
   Для Entra используйте tenant-specific v2 issuer и scope зарегистрированного
   Gateway API с v2 access tokens (`requestedAccessTokenVersion: 2`). Версия и
   issuer должны совпадать с реально выдаваемым access token. Claim `roles`
   должен присутствовать именно в access token.
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
`issuer + sub + audience` к internal user ID. Email не используется для привязки,
автоматического создания пользователей и назначения ролей нет. Ownership
существующего subject неизменяем. Пустые allowed models/tools запрещают inference
и tools; для доступа к моделям выдайте явные grants. Изменение существующей
привязки заменяет переданные grants, поэтому проверяйте её policy перед сохранением.

Нажмите **Test sign-in**, войдите в открывшейся вкладке как тот же internal user,
которому принадлежит текущий admin key, и вернитесь к **Refresh test status**.
Проверка имеет срок пять минут и требует актуальную роль `admin`, подписанный
access token, точные issuer/audience и действующую directory binding. Тест не
создаёт активную browser session и не заменяет текущий вход администратора.

После статуса `passed` нажмите **Activate SSO**. При активации Auth повторно
проверяет directory policy: деактивация пользователя или смена прав после теста
отклоняет активацию. Запоздавший proof, повторный callback и stale revision
отклоняются. Оператор после 409 должен перечитать настройку и повторить действие.

## Действие на систему и восстановление

Активация меняет JWT issuer, audience и role policy **для всего Gateway**, включая
inference и внешние приложения. В текущем контракте поддерживается один issuer;
multi-issuer federation отсутствует. Virtual keys сохраняют независимый доступ.
Все Auth/Gateway реплики читают общий PostgreSQL document; настройки не требуют
рестарта или правки ConfigMap после подготовки runtime dependencies.

**Disable browser SSO** отключает browser login и cookies, сохраняя JWT trust для
других клиентов. **Roll back** восстанавливает один предыдущий managed profile;
rollback первой активации возвращает environment JWT/browser configuration.
Хранится только один предыдущий профиль, а не полная история. При недоступности
Auth/БД или неверном encryption key JWT/cookie вход закрывается. Virtual key не
зависит от SSO profile и позволяет восстановить настройку при исправном Auth;
stored keys также требуют доступной БД. Полный отказ Auth не обходится этим
механизмом. Logout работает и при сбое Auth.

Без managed active profile прежние environment SSO/JWT настройки продолжают
использоваться. Новый профиль хранится AES-GCM encrypted в одной bounded строке
`auth_sso_settings`: active, previous, draft и одна test attempt. JWKS verifier
cache хранит один активный профиль на реплику, сетевой I/O выполняется вне lock.

Сессия использует encrypted HttpOnly cookie с bounded lifetime и лимитом access
token 2800 байт; refresh token не хранится. Время входа ограничено меньшим из
configured TTL и token expiry. При истечении нужен повторный вход; oversized token
отклоняется. SAML, refresh sessions и автоматический JIT provisioning сюда не входят.

## Проверки

Regression tests покрывают encryption/redaction, CAS и concurrent replicas,
invalid URL/role/scopes, proof replay/expiry, same-admin check, deprovisioning,
API-key recovery, cookie rotation, outage fail-closed и отсутствие session в тесте.
`scripts/test-postgres-integration.sh` выполняет PostgreSQL и race tests.
`scripts/test-identity-integration.py` выполняет реальный Keycloak PKCE flow для
черновика и активного входа, активацию, запрет trust changes из SSO session,
disable и rollback на отдельной тестовой БД; этот сценарий входит в CI.

Контракты IdP: [Microsoft OIDC endpoints](https://learn.microsoft.com/en-us/entra/identity-platform/v2-protocols-oidc),
[Microsoft access token claims](https://learn.microsoft.com/en-us/entra/identity-platform/access-token-claims-reference).
[Версия access token](https://learn.microsoft.com/en-us/entra/identity-platform/access-tokens)
задаётся зарегистрированным API.
Реальный Entra tenant в локальных проверках не использовался.
