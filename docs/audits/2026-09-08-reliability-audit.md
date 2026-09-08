**Аудит надежности AI Gateway — 8 сентября 2026**

Это исходный аудит до исправлений. Реализованное поведение и ограничения описаны в [production reliability](../production-reliability.md); перечисленные ниже дефекты отражают состояние исходного commit.

**Основание и границы проверки**

- AI Gateway: commit `17c0fcdf7791b1fc0d6bf1e1342d0467d7566b04`; рабочее дерево перед аудитом чистое.
- Выборочно прослежены inference → auth/access → routing/cache/guardrails → billing, управление состоянием, UI components/pages, OpenAPI и CI. Это инженерный аудит ключевых сценариев, не исчерпывающий pentest всех строк репозитория.
- UI проверен по исходникам и компонентным тестам; в браузере открыт экран входа локального demo gateway. Вход автоматическая проверка отклонила; разрешение запрошено. Визуальная проверка внутренних экранов, responsive layout и поведения настоящего браузера после входа не выполнена.
- Публичные LLM, реальные ключи, production-сервисы и контейнеры не использовались. Исходники продукта, конфигурации и зависимости не изменены.

**Что уже реализовано и не является gap**

Подтверждены Chat Completions, Responses, Embeddings, Rerank; native/synthetic SSE; priority/weight/adaptive routing, retries, cooldown, cross-model fallbacks; модель Providers → Credentials → Deployments → Model Groups; virtual keys, JWT/JWKS, scopes и access groups; PostgreSQL budgets и durable outbox; exact/semantic cache; Prometheus/OTLP; request/audit logs и guardrail monitor. Есть model onboarding с preview, проверка provider connectivity и discovery. В UI реализованы серверная пагинация virtual keys и курсорная пагинация logs — утверждать, что вся пагинация клиентская, было бы неверно.

Сильная сторона проекта — ограничение передачи чувствительных данных между сервисами и metadata-only observability. Отсутствие хранения prompts/responses и запрет remote image URLs — заявленные границы продукта, а не автоматически дефекты.

**Подтвержденные недочеты**

Приоритет P1 означает исправление до серьезной production-нагрузки в затронутом режиме. P2 — следующая итерация. Оценка не является утверждением о наблюдавшемся production-инциденте.

**F01 · P1 · `max_completion_tokens` не учитывается в TPM и reserve бюджета.**

Источники: [estimateChatTokens](../../repos/gateway/internal/gateway/access.go), [requestedOutputTokens](../../repos/gateway/internal/modules/billing_remote.go), [ChatCompletions](../../repos/gateway/internal/gateway/handler.go).

Для сообщения `test` и `max_completion_tokens=10000` TPM-оценка равна **1**, а для `max_tokens=10000` — **10001**. Billing helper возвращает соответственно **0** и **10000** output tokens. Поле современного API проходит к upstream, но оба helper читают только legacy-поле. При remote billing резерв может ограничиться default, а commit уже учтет фактический расход. Также TPM-оценка не включает tool definitions: описание инструмента из 40000 символов не изменило оценку.

Решение: единый нормализатор output limit для handler, billing и adapters; добавить учет tool schemas и прочего токенизируемого контекста. Для запросов без limit определить явную консервативную политику reserve и reconciliation. Приемка: одинаковые лимиты в двух допустимых полях дают одинаковый TPM/reserve; tools влияют на оценку; budget rejection происходит до upstream. Изменение делает enforcement строже и должно быть отражено в release notes.

**F02 · P1 при включенном memory cache · Неограниченное удержание записей.**

Источники: [exactCache](../../repos/gateway/internal/provider/cache.go), [memoryAffinity](../../repos/gateway/internal/provider/affinity.go), [LifecycleStore](../../repos/billing/internal/modules/storage.go), [BillingModule.Handle](../../repos/billing/internal/modules/billing.go).

Exact cache и affinity удаляют expired entry только при чтении того же key; `set` не делает global pruning/eviction. После 1000 уникальных записей, перевода тестовых часов за TTL и новой записи обе map содержат **1001** entry. Ограничение `maxBytes` относится к одной записи, не всей map. В nondurable billing после 1000 завершенных запросов остаются **2000** phase IDs без TTL. Результат — рост памяти от исторического трафика; OOM под нагрузкой не измерялся. Semantic cache уже имеет pruning и общий entry cap — его этот вывод не касается.

Решение: общий byte/entry budget, TTL eviction при записи или обслуживающий worker с shutdown; ограниченный dedup retention для lifecycle. Redis уменьшает этот риск для cache/affinity, durable billing обходит memory lifecycle. Приемка: уникальный трафик и продвижение fake clock не приводят к неограниченному числу retained entries; проверить heap и race detector.

**F03 · P1 в nondurable billing · Клиентский request ID используется как глобальная идентичность billing-события.**

Источники: [requestID](../../repos/gateway/internal/gateway/handler.go), [BillingModule.Handle](../../repos/billing/internal/modules/billing.go), [PostgresOutboxRepository](../../repos/billing/internal/modules/postgres_outbox.go).

Gateway принимает `X-Request-ID`, а billing dedup key строится как `RequestID + ':' + phase`. Изолированный тест двух разных users с одинаковым request ID в nondurable режиме записал **одно** commit-событие. Это может занижать usage даже при случайном повторе correlation ID. Durable ledger тоже использует event ID без tenant, но полный эффект зависит от включенного policy checker. PostgreSQL budgets проверяют owner и отклоняют reserve после завершенного lifecycle; вывод о простом обходе всех durable budgets не делается.

Решение: разделить внешний correlation ID и уникальный внутренний execution ID. Дедупликация должна сохраняться для повторной доставки одного execution, а повторный HTTP inference должен либо получать новый execution, либо обрабатываться полноценным tenant-scoped idempotency-контрактом с request fingerprint. Приемка: два независимых запроса не исчезают из учета; повторная доставка одного commit учитывается один раз; concurrent duplicates проверяются отдельно.

**F04 · P2, P1 для обязательного учета · Memory outbox теряет события без сигнала о конечном отказе.**

Источники: [AsyncUsageOutbox.run / NewUsageEventWriter](../../repos/billing/internal/modules/storage.go), [BillingModule.Close](../../repos/billing/internal/modules/billing.go), [settings](../../repos/billing/internal/modules/settings.go).

После исчерпания retries worker переходит к следующему событию без dead-letter queue, счетчика окончательной потери или возврата ошибки отправителю. У worker нет Close/drain/cancel; accepted in-memory queue теряется при остановке процесса. Это вывод из кода; outage/restart эксперимент не выполнялся. Durable outbox уже реализован, но выключен по умолчанию. При его включении конструктор все равно сначала создает неиспользуемый memory writer/worker.

Решение: для надежного учета выбирать существующий durable path, не запускать лишний worker; для best-effort режима добавить dropped/failed metrics и bounded shutdown. Приемка: permanent writer error наблюдаем, restart durable worker доставляет событие, shutdown не оставляет goroutine. Production-default менять явно, с обновлением документации.

**F05 · P2 · Exact cache слабее заявленного контракта изоляции.**

Источники: [providerCacheKey](../../repos/gateway/internal/provider/cache.go), [semanticRequest](../../repos/gateway/internal/provider/semantic_cache.go), [cache contract](../inference.md).

При наличии TeamID exact key использует team вместо credential, затем request JSON. UserID, credential ID, tags, effective policy revision в key не входят. Тест подтвердил одинаковый exact key для разных users/credentials одной team и разного policy metadata. При этом guardrails запускаются перед cache lookup: совпадение key само по себе не доказывает обход scanner или раскрытие данных. Но это противоречит описанному credential/identity/policy scope и допускает повторное использование старого ответа после изменения контекста policy.

Решение: явная cache-scope policy, по умолчанию credential+user+effective-policy fingerprint; shared-team cache только как сознательная настройка. Инвалидировать по semantic revision, избегая reset на несвязанных admin-изменениях. Приемка: matrix same/different credential, policy, model, deployment и anonymization; обновление потребует смены namespace cache.

**F06 · P2 · Overview показывает размер страницы вместо общего количества virtual keys.**

Источники: [OverviewPage](../../repos/gateway/ui/src/pages/OverviewPage.tsx), [ListVirtualKeys](../../repos/gateway/internal/gateway/management.go).

API по умолчанию возвращает до 100 keys и поле `total`; Overview берет `data.length`. Компонентный тест с `data.length=100, total=250` показал **100**. Решение: использовать `total` или aggregate inventory endpoint. Приемка: 0/100/250 keys, отсутствие total у непагинированных endpoints и корректный fallback без ложного полного count.

**F07 · P2 · Отказ одного inventory endpoint скрывает весь Overview.**

Источник: [OverviewPage](../../repos/gateway/ui/src/pages/OverviewPage.tsx).

Шесть запросов выполняются через `Promise.all`; ошибка любого переводит весь экран в error branch. В тесте keys вернули 503, остальные endpoints успешны, но карточка Providers исчезла. Это особенно мешает модульному продукту при частично подключенном backend. Решение: независимые состояния карточек/`allSettled`, локальный Retry и различение not configured / unavailable / empty. Приемка: отказ Auth не скрывает здоровый routing inventory; восстановление одной карточки не сбрасывает остальные.

**F08 · P2 · Запоздавший Usage-запрос подменяет отчет под новыми фильтрами.**

Источник: [UsagePage load / inspectDimension](../../repos/gateway/ui/src/pages/UsagePage.tsx).

У load нет AbortController или sequence guard. Тест задержал первый запрос, применил новый model filter, получил новый отчет, затем завершил старый. Экран показал **old-model**, хотя поле фильтра осталось **new-model**. У drilldown аналогичный шаблон кода; отдельный runtime-тест drilldown не выполнялся.

Решение: latest-request-wins с отменой и generation ID; разделить состояния main report и drilldown. Сохранять подтвержденные фильтры в URL для воспроизводимых ссылок. Приемка: deterministic deferred-promise тест не допускает старый отчет/ошибку поверх нового.

**F09 · P2 · Общая модальная форма не обеспечивает keyboard lifecycle.**

Источник: [ResourceForm](../../repos/gateway/ui/src/components/ResourceForm.tsx).

`role=dialog` и `aria-modal` выставлены, но нет управления focus/return focus и Escape. Компонентный тест подтвердил, что при открытии focus вне dialog, Escape не вызывает onClose. Полный Tab-cycle в реальном браузере не проверен. Решение: общий dialog на уже используемой Gravity UI библиотеке с начальным фокусом, focus containment/restore и предсказуемым Escape; защитить dirty form от случайной потери. Приемка: keyboard-only create/edit/close и возврат фокуса на инициатор.

**F10 · P2 · CSV экспорт использует JSON escaping.**

Источник: [UsagePage.exportCSV](../../repos/gateway/ui/src/pages/UsagePage.tsx).

`JSON.stringify` не является CSV encoder: кавычки становятся `\"` вместо CSV `""`, перенос строки — буквальным `\n`. Проверка текущего алгоритма через CSV parser не восстановила исходное `team "A", finance`. Решение: единая CSV-cell функция с doubled quotes и round-trip тестами для quote/comma/newline/Unicode. Для экспорта в электронные таблицы отдельно определить обработку formula-like strings. Открытие файла в Excel не выполнялось.

**F11 · P2 · Неверсионированные lazy CSS кэшируются на год как immutable.**

Источники: [vite assetFileNames](../../repos/gateway/ui/vite.config.ts), [admin UI asset headers](../../repos/gateway/internal/gateway/admin_ui.go).

Production build создает `app2.css` и `app3.css` без content hash, потому что всем CSS задано имя `app.css` и bundler разрешает конфликт суффиксом. Только `app.css` имеет специальный no-store route. HTTP-тест `/ui/assets/app2.css` вернул **200** и `public, max-age=31536000, immutable`. После обновления содержимого под тем же URL браузер может оставить старые стили. Решение: content hash для lazy CSS или revalidation для всех unhashed assets. Приемка: изменение CSS меняет URL либо требует revalidation; rolling deployment не оставляет старые стили. Полный двухрелизный browser-тест не выполнялся.

**F12 · P2 · CI не исполняет важные PostgreSQL-интеграционные проверки.**

Источники: [CI workflow](../../.github/workflows/test.yml), [controlstore integration](../../repos/gateway/internal/controlstore/postgres_test.go), [auth integration](../../repos/auth/internal/modules/postgres_keys_test.go), [billing integration](../../repos/billing/internal/modules/postgres_budgets_test.go).

Тесты пропускаются без `CONTROL_PLANE_POSTGRES_TEST_DSN`, `AUTH_POSTGRES_TEST_DSN`, `BILLING_POSTGRES_TEST_DSN`. Go job запускает race/coverage, но не имеет PostgreSQL service и этих env. Поэтому зеленый CI не проверяет реальный SQL lifecycle, locking, migration compatibility и outbox delivery.

Решение: отдельный обязательный integration job с временным PostgreSQL и применением migrations каждого сервиса; fail при неожиданном skip. Добавить browser E2E через production Go handler для CSP/assets/focus: jsdom не проверяет HTTP headers. Приемка: lifecycle, parallel reserve, tenant collision и outbox replay выполняются на реальной тестовой БД.

**Предлагаемый порядок работ**

| Этап | Содержание | Проверяемый результат |
| --- | --- | --- |
| 1. Корректность учета | F01, F03, integration job из F12 | Современные token limits работают одинаково с legacy; независимые executions не дедуплицируются; реальный PostgreSQL участвует в CI. |
| 2. Надежность | F02, F04, F05 | Память ограничена; потери outbox наблюдаемы/восстанавливаемы; cache scope документирован и протестирован. |
| 3. Достоверность интерфейса | F06–F11 | Правильные totals; частичный отказ не ломает весь обзор; stale fetch не подменяет отчет; формы доступны с клавиатуры; CSV и CSS updates корректны. |
| 4. Целевые расширения | Provider quotas, SSO, выбранный native adapter; MCP/output guardrails по требованию | Для каждой функции есть отдельный acceptance scenario и compatibility matrix, а не только пункт меню. |

Практичные небольшие изменения: F01 и UI F06/F07/F08/F10/F11. F03/F05 затрагивают семантику идентичности и cache namespace — требуют отдельного design review. SSO/MCP требуют явно согласованной новой границы авторизации. Это предложения; автоматических исправлений и изменений security settings аудит не выполнял.

**Выполненные проверки и доказательства**

| Проверка | Результат |
| --- | --- |
| `gofmt -w .` в каждом из шести Go modules | Успешно; изменений Go-файлов нет. |
| `go vet ./...`, `go test ./...`, `go build ./...` во всех шести modules | Успешно на системном Go 1.26.4 и повторно на настоящем Go 1.25.13 (`GOTOOLCHAIN=local`), соответствующем declared Go 1.25 baseline. Homebrew alias `go@1.25` фактически указывал на 1.26.4, поэтому использован cached 1.25.13 toolchain. |
| `go test -race ./...` во всех шести modules | Успешно на Go 1.26.4. Race detector на Go 1.25 отдельно не повторялся. |
| UI `npm run typecheck` | Успешно. |
| UI `npm test -- --reporter=dot` | 38 файлов, 132 теста прошли. |
| UI `npm run build -- --outDir /tmp/ai-gateway-audit-ui-build` | Успешно, Node 26.3.1. Vite предупреждает об entry chunk 715.41 kB / gzip 229.02 kB; это сигнал для измерения загрузки, не доказанный UX-дефект. |
| Дополнительные Go evidence tests через `go test -overlay ... -run TestAuditEvidence -v` | 7 проверок воспроизвели текущие дефекты F01/F02/F03/F05/F11 без изменения production sources. |
| Дополнительные UI evidence tests | 4 проверки воспроизвели F06/F07/F08/F09; временный файл после выполнения убран. |
| CSV round-trip текущего выражения через Python CSV parser | Quotes/newline не восстанавливаются; F10. |

Evidence tests намеренно проверяли наблюдаемое дефектное поведение: их PASS означает воспроизведение проблемы, не исправление продукта. При реализации нужны regression tests на правильное поведение.

Локальные материалы текущей сессии: `/tmp/ai-gateway-audit-evidence/` (Go overlay и UI test source), `/tmp/ai-gateway-go-audit-checks.log`, `/tmp/ai-gateway-go12513-audit.log`, `/tmp/ai-gateway-race-audit.log`, `/tmp/ai-gateway-ui-audit-tests.log`, `/tmp/ai-gateway-ui-evidence.log`. Они временные; ключевые результаты сохранены выше.

Не выполнены: PostgreSQL/ClickHouse integration без подготовленных DSN, browser E2E после входа, mobile/visual/a11y browser-аудит, live provider/OIDC/MCP tests, stress/restart эксперименты, сравнительный benchmark, container build и dependency vulnerability audit. Контейнерные определения не менялись. Эти ограничения не отменяют воспроизведенные unit/component findings, но ограничивают заявления о production readiness.
