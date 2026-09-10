# MCP: интеграция и границы контроля

Gateway реализует реестр MCP, разрешения на инструменты, безопасное discovery
через `GET /v1/mcp/servers/{id}/tools` и passthrough remote MCP connectors через
Responses API. Discovery выполняет `initialize` и `tools/list` через bounded
Streamable HTTP client. Прямой `POST /v1/mcp/servers/{id}/tools/{tool}` выполняет
`tools/call` с обязательной идемпотентностью. Запуска stdio-процессов в gateway нет.

## Два сценария

| Сценарий | Что получает gateway | Кто подключается к MCP и выполняет инструмент | Capability маршрута |
| --- | --- | --- | --- |
| OpenCode через `/v1/chat/completions` | Описания `type: function`, затем сообщения с результатами | OpenCode, со своими MCP credentials | `tools`, плюс `stream` для streaming |
| Remote MCP через `/v1/responses` | `type: mcp` с label, URL и настройками коннектора | Upstream provider модели | Явная `mcp`, плюс `stream` для streaming |
| Gateway discovery | ID включенного MCP Server и optional cursor | Gateway выполняет `initialize` и `tools/list` без credentials | Не зависит от model route |
| Gateway tool call | ID включенного MCP Server, имя tool и JSON arguments | Gateway выполняет `initialize` и один `tools/call` без credentials | Не зависит от model route |

В первом сценарии клиент получает список инструментов у своего MCP-сервера,
передаёт их описания модели через gateway, получает `tool_calls`, выполняет
вызовы и отправляет результаты в следующем запросе. Gateway проверяет
объявленные имена функций; он не проверяет протокол прямого соединения клиента
с MCP. Имя функции с префиксом `mcp.` само по себе не доказывает её происхождение.

Во втором сценарии OpenAI-compatible adapter передаёт определение коннектора
провайдеру. Поддержка passthrough в adapter и capability `mcp` не гарантируют,
что конкретный upstream API умеет remote MCP. Gateway не преобразует Chat
Completions tools в Responses MCP и не добавляет записи реестра в запросы.

## Разрешения

Virtual Key использует `allowed_tools`. Все объявленные в запросе инструменты
должны быть разрешены: один запрещённый инструмент отклоняет весь запрос с
HTTP 403, даже если модель не собиралась его вызывать. Неверное определение
инструмента возвращает HTTP 400.

- Для Chat Completions проверяется `function.name`, например `weather_forecast`.
- Для Responses function проверяется `name`.
- Для Responses MCP проверяется `mcp:<server_label>@<canonical-https-url>`.
  HTTPS URL не может содержать userinfo, query или fragment; host приводится
  к нижнему регистру, завершающий slash удаляется при построении identity.
- Для gateway runtime отдельный tool имеет identity
  `mcp:<server_id>@<canonical-https-url>#tool:<tool_name>`. Connector identity
  разрешает все tools сервера; tool identity разрешает только совпавшее имя.
- Grants поддерживают точное совпадение, `*` и префикс с завершающей `*`.
  Только точный connector grant ограничивает конкретную пару label/URL.
- Пустой `allowed_tools` не ограничивает инструменты со стороны ключа.
- `toolset:weather` разрешает инструменты через включённый Toolset `weather`.
  Для connector identity дополнительно нужен matching grant в поле `tools`
  хотя бы одного включённого MCP Server. Для обычного имени функции эта
  дополнительная проверка сервера не выполняется.
- Текущий matcher трактует пустой `tools` Toolset как разрешение любого
  идентификатора (для connector остаётся проверка включённого сервера).
  Пустой `tools` включённого MCP Server также совпадает с любым connector.
  Поэтому пустые списки не подходят для deny-all: задавайте явные grants
  или отключайте запись. Это поведение кода, а не проверка принадлежности
  коннектора адресу сервера.
- Назначенные Access Groups проверяются отдельно: объединение grants
  включённых групп пересекается с разрешениями ключа. Пустое групповое
  разрешение инструментов ничего не разрешает; missing/disabled assignment
  блокирует запрос.

Отключение Toolset закрывает только путь через этот toolset. Прямой grant,
`*`, пустые grants ключа или другой разрешающий toolset могут сохранить доступ
при выполнении остальных проверок. Отключение MCP Server не является глобальным
запретом его URL. Изменения действуют после загрузки актуального registry state
и не отменяют уже выполняющиеся операции.

Поле `tools` в реестре содержит строки разрешений, а не JSON Schema функций.
API ограничивает число и длину строк, но не проверяет, что они соответствуют
указанным label/URL или реальному списку инструментов сервера. Совпадение с
включённым сервером проверяется по его `tools`, а не по metadata `server_url`.

## Пример: remote MCP через Responses

Все URL ниже демонстрационные. Нужен реально доступный провайдеру MCP-сервер
и модель с поддержкой remote MCP. Через административный API сохраните:

`PUT /admin/v1/mcp/servers/weather`

```json
{
  "label": "weather-prod",
  "server_url": "https://mcp.example.test",
  "transport": "streamable-http",
  "tools": ["mcp:weather-prod@https://mcp.example.test"],
  "enabled": true
}
```

`PUT /admin/v1/mcp/toolsets/weather`

```json
{
  "name": "Weather connector",
  "tools": ["mcp:weather-prod@https://mcp.example.test"],
  "enabled": true
}
```

В политике Virtual Key задайте `"allowed_tools": ["toolset:weather"]` и
разрешённую модель. Если у ключа есть Access Groups, их policy также должна
разрешать этот коннектор. Затем клиент отправляет `/v1/responses`:

```json
{
  "model": "your-public-model",
  "input": "What is the weather in Paris?",
  "tools": [{
    "type": "mcp",
    "server_label": "weather-prod",
    "server_url": "https://mcp.example.test",
    "allowed_tools": ["forecast"],
    "require_approval": "always"
  }]
}
```

`allowed_tools` внутри коннектора ограничивает remote tools у провайдера; это
другое поле, чем `allowed_tools` Virtual Key. Gateway разрешает коннектор
целиком и не пересекает список `forecast` с registry grants. Политику
`require_approval` выполняет провайдер; клиент должен поддерживать его workflow
подтверждений. Gateway не предоставляет отдельный UI подтверждения MCP-вызовов.

Для OpenCode через Chat Completions настраивайте MCP на стороне OpenCode,
а в grants ключа указывайте точные имена функций, которые клиент отправляет
в `tools`. Connector grant вида `mcp:...@https://...` не заменяет grant имени
функции в этом сценарии.

## Credentials, аудит и эксплуатация

Реестр не хранит credentials, headers или secrets. В Responses клиент может
передать scoped `headers` внутри коннектора: они отправляются выбранному
провайдеру вместе с определением MCP. Bearer Virtual Key gateway не подставляет
в них автоматически. В клиентском сценарии credentials принадлежат клиенту.

Изменения реестра аудируются; сохранение после рестарта использует общий
durable admin-state snapshot, если он настроен. `expand=references` показывает
зависимости. Удаление сервера с зависимым toolset запрещено; удаление toolset
проверяет Access Groups и полную выборку non-revoked Virtual Keys и блокируется
при назначении или невозможности подтвердить полноту выборки.

Discovery проходит authentication, connector ACL, отдельное пересечение grants
Access Groups и RPM admission. Оно фиксируется durable billing lifecycle с
`api_type=mcp_tools_list`, нулевыми токенами и стоимостью. В Request Logs и
usage reports доступен отдельный `tool_requests`; discovery оставляет его
нулевым, потому что инструмент не выполнялся. Список discovery фильтруется по
трем слоям: grants MCP Server, Virtual Key и Access Groups. Tool-specific grant
дает право выполнить discovery соединения, но в ответе остаются только
разрешенные tool definitions.

Прямой tool call проходит те же authentication, connector ACL, Access Groups и
RPM checks. Клиент обязан передать один `Idempotency-Key` длиной до 128 visible
ASCII characters. Ключ scoped по credential, server ID и tool name. Gateway
хранит request hash, первый execution ID и итоговый HTTP response в PostgreSQL
24 часа. Повтор с теми же arguments получает сохраненный ответ без нового
вызова и billing; другой payload получает `409 idempotency_conflict`, а
параллельный незавершенный вызов — `409 idempotency_in_progress`.

До сетевого вызова gateway обязан записать audit attempt; недоступный audit
блокирует выполнение. Audit содержит server ID и tool name, но не arguments,
result или сам idempotency key. Успешный protocol result учитывается как один
`tool_requests`, включая результат с `isError=true`; transport/protocol failure
закрывает billing через cancel и сохраняется для безопасного replay.

Bounded Streamable HTTP client выполняет initialize negotiation, поддерживает
JSON и SSE ответы на POST,
передает protocol/session headers, ограничивает request/response/tool pages и
отклоняет private, loopback и link-local адреса при каждом DNS resolve. Реестр
не хранит credentials, поэтому discovery и прямое выполнение работают только с
серверами, которым они не нужны.

## Проверка реализации

Источники контрактов:

- [Авторизация инструментов](../repos/gateway/internal/gateway/access.go).
- [Реестр, grants и зависимости](../repos/gateway/internal/gateway/mcp_registry.go).
- [Маршрутизация по capabilities](../repos/gateway/internal/provider/provider.go).
- [Passthrough adapter](../repos/gateway/internal/provider/openai_compatible.go).
- [Swagger / OpenAPI](../repos/gateway/api/openapi.yaml).

Из `repos/gateway` запустите `go test ./api ./internal/gateway ./internal/provider`.
Проверки покрывают API-контракты, ACL, состояние toolsets, безопасные metadata,
зависимости удаления и routing. Они не подтверждают доступность внешнего MCP
или поддержку remote MCP конкретным провайдером — это отдельный интеграционный
тест с тестовым сервером и ограниченным инструментом.
