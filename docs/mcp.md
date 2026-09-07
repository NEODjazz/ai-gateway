# MCP: интеграция и границы контроля

Gateway реализует реестр MCP, разрешения на инструменты и passthrough remote
MCP connectors через Responses API. Собственного MCP endpoint, клиента для
`initialize`, `tools/list`, `tools/call`, запуска stdio-процессов и исполнения
инструментов в gateway нет. Добавление MCP Server в UI сохраняет metadata,
но не подключает сервер к OpenCode или провайдеру.

## Два сценария

| Сценарий | Что получает gateway | Кто подключается к MCP и выполняет инструмент | Capability маршрута |
| --- | --- | --- | --- |
| OpenCode через `/v1/chat/completions` | Описания `type: function`, затем сообщения с результатами | OpenCode, со своими MCP credentials | `tools`, плюс `stream` для streaming |
| Remote MCP через `/v1/responses` | `type: mcp` с label, URL и настройками коннектора | Upstream provider модели | Явная `mcp`, плюс `stream` для streaming |

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

Request Logs и budgets относятся к запросам модели. Это не отдельный аудит
каждого `tools/call` и не бюджет на прямые клиентские обращения к MCP.
Централизованные MCP discovery, credentials/OAuth, egress policy и аудит
исполнения потребуют отдельного MCP proxy runtime.

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
