# Inference API и совместимость

## Публичные endpoints

Gateway реализует OpenAI-compatible endpoints:

| Endpoint | Назначение |
| --- | --- |
| `GET /v1/models` | Модели, доступные текущему credential |
| `POST /v1/chat/completions` | Chat, tools, structured output и vision |
| `POST /v1/responses` | Responses, continuity, function tools и MCP passthrough |
| `POST /v1/embeddings` | String или массив строк |
| `POST /v1/rerank` | Query/documents ranking |

Полные payloads, ограничения и ошибки описывает
[OpenAPI](../repos/gateway/api/openapi.yaml). Все endpoints требуют Bearer
credential и применяют тот же model/tool policy, что `/v1/models` и Playground.

## Матрица полей запроса

JSON decoder применяет закрытый контракт request types, указанный в OpenAPI
(`additionalProperties: false`). Неизвестные поля верхнего уровня возвращают HTTP 400 `invalid_request` с сообщением
`json: unknown field "имя"` до выполнения pipeline и provider call.
Это намеренное изменение совместимости: раньше неизвестные поля игнорировались.
В частности, `reasoning_effort`, `logprobs` и `service_tier` не поддерживаются
и должны быть удалены из запроса; они не передаются upstream.

| Endpoint | Поля контракта верхнего уровня |
| --- | --- |
| `/v1/chat/completions` | `provider`, `model`, `messages`, `tools`, `tool_choice`, `parallel_tool_calls`, `response_format`, `stream`, `max_tokens`, `max_completion_tokens`, `temperature`, `top_p`, `stop`, `seed` |
| `/v1/responses` | `provider`, `model`, `input`, `instructions`, `tools`, `tool_choice`, `parallel_tool_calls`, `text`, `previous_response_id`, `stream`, `max_output_tokens`, `max_tokens`, `temperature`, `top_p` |
| `/v1/embeddings` | `provider`, `model`, `input`, `encoding_format`, `dimensions`, `user` |
| `/v1/rerank` | `provider`, `model`, `query`, `documents`, `top_n`, `rank_fields`, `return_documents`, `max_chunks_per_doc`, `max_tokens_per_doc` |

Матрица описывает входной контракт gateway; возможности конкретной модели и
adapter дополнительно ограничивают допустимые запросы. `provider` управляет
выбором adapter. Свободные JSON-объекты, например `tools[].function.parameters`
и `response_format.json_schema.schema`, сохраняют произвольные свойства:
имена полей пользовательской схемы не считаются параметрами inference.

## Capabilities

Поддерживаемые значения: `chat`, `responses`, `embeddings`, `rerank`, `stream`,
`tools`, `structured_output`, `mcp`, `vision`. Gateway выводит требования из
request и исключает несовместимые deployments до provider call.

Legacy endpoint без capabilities сохраняет совместимость с базовыми chat,
responses и embeddings flows, но не является неявным opt-in для `mcp`, `vision`
или `rerank`. Для новых deployments задавайте capabilities явно.

Vision принимает только inline `data:image/{jpeg,png,gif,webp};base64,...`.
Remote URLs запрещены. AV должен быть включён; media type проверяется по
signature. Лимиты: 8 изображений, 8 MiB каждое, 16 MiB decoded total и 24 MiB
на JSON body.

## Provider adapters

| Type | Особенности |
| --- | --- |
| `openai`, `openai-compatible`, `openrouter` | OpenAI wire format; Azure-style base URL поддерживается |
| `anthropic` | Преобразование chat/tools/vision в native Messages API |
| `ollama` | Native chat/stream/embeddings |
| `demo` | Локальный deterministic fallback для разработки |

OpenAI-compatible adapter один раз повторяет запрос с
`max_completion_tokens`, только когда upstream явно отверг legacy
`max_tokens`. Unsupported non-default `temperature` не переписывается
молча: клиент должен отправить допустимое для модели значение.

## Routing и модели

Public model может ссылаться на упорядоченную Model Group. `priority` задаёт
fallback tiers, `weight` распределяет трафик внутри tier, adaptive strategy
учитывает EWMA latency/failures. Same-deployment retry разрешён только для
transient failure classes и учитывает bounded backoff/`Retry-After`.

Cross-model fallbacks настраиваются отдельно для `general`, `context_window` и
`content_policy`. Gateway заранее пересекает все цели с model grants и
guardrails. Authentication, invalid request, gateway content rejection и budget
rejection являются terminal и не обходятся fallback-ом.

Public model остаётся billing identity; deployment ID, provider и upstream
model записываются отдельными dimensions.

## Streaming

При `stream: true` совместимый adapter проксирует native stream. Если adapter
сообщает об отсутствии native streaming до первого события, gateway выполняет
один non-streaming call и синтезирует SSE без повторного inference.

Retry/failover возможен только до первой клиентской записи. После неё ошибка
завершает текущий stream. Gateway flush-ит принятые события, но не дробит
крупный upstream chunk. Request-scoped deanonymizer восстанавливает text и tool
argument deltas до отправки, включая placeholder, разделённый между событиями.

## Cache

Exact cache и semantic cache выключены по умолчанию. Cache scope включает
credential/identity, public model, normalized request и policy context. Semantic
cache применяется только к поддерживаемому non-streaming text-only chat и
использует отдельный embedding credential, а не клиентский Bearer.

Gateway cache hit и provider prompt-cache tokens — разные метрики. Billing и
Logs сохраняют `cache_status`/`cache_kind`, а также отдельные
`cache_read_input_tokens` и `cache_write_input_tokens`.

## Adapter parameter policy

Параметр должен сохранять смысл при преобразовании adapter. Поля, которые
текущая реализация native adapter не передаёт, возвращают HTTP 400
`unsupported_parameter` с `param`. Проверка выполняется до provider modules,
резервирования billing, cache lookup и отправки upstream. Такая ошибка является
терминальной и не запускает retry/fallback с потерей параметра. Прямые вызовы
adapter используют те же проверки, включая streaming.

| Adapter / endpoint | Явно отклоняемые поля |
| --- | --- |
| Anthropic chat | `stop`, `seed`, `parallel_tool_calls` |
| Anthropic Responses | `previous_response_id`, `parallel_tool_calls` |
| Ollama native chat | `tool_choice`, `parallel_tool_calls` |
| Ollama embeddings | `user`; `encoding_format`, отличный от `float` |

Остальные верхнеуровневые поля действующего OpenAI-compatible контракта
передаются соответствующим upstream wire request. Это не подтверждает поддержку
параметра каждой моделью: upstream может вернуть собственную ошибку.
Native adapter ограничения выше являются изменением совместимости для клиентов,
которые раньше отправляли эти поля и получали ответ с молча потерянной настройкой.

OpenAI-compatible SSE учитывает `usage` из финального события с пустым `choices`:
reported prompt/completion/total tokens и prompt-cache details доходят до
post-response billing. Это событие не создаёт дополнительный choice и сохраняется
при проксировании клиенту. Если upstream не присылает usage, остаётся существующий
estimated fallback; точный учет не выводится из отсутствующих данных.
