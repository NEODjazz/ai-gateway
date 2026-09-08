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
Например, `service_tier` и `background` пока не поддерживаются и не передаются
upstream. Новые поддерживаемые параметры перечислены ниже.

| Endpoint | Поля контракта верхнего уровня |
| --- | --- |
| `/v1/chat/completions` | `provider`, `model`, `messages`, `tools`, `tool_choice`, `parallel_tool_calls`, `response_format`, `stream`, `max_tokens`, `max_completion_tokens`, `temperature`, `top_p`, `stop`, `seed`, `reasoning_effort`, `logprobs`, `top_logprobs`, `frequency_penalty`, `presence_penalty`, `logit_bias` |
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
| `gemini` | Native GenerateContent chat/stream, tools, inline vision, structured output; API key |
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
| Anthropic chat | `seed`; `stop` неверного типа или более четырёх последовательностей |
| Anthropic Responses | `previous_response_id` |
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


## Chat generation controls

OpenAI-compatible chat передаёт `reasoning_effort`, `logprobs`, `top_logprobs`,
`frequency_penalty`, `presence_penalty` и `logit_bias` в обычном и streaming flow.
Явные `false` и `0` сохраняются. Gateway проверяет диапазоны и зависимость
`top_logprobs` от `logprobs=true`; конкретная модель дополнительно проверяет
поддержку каждого значения. Anthropic, Ollama и demo отклоняют эти новые controls
как `unsupported_parameter`, пока для них нет соответствующего преобразования.

Logprobs сохраняются в JSON response, проксируемом SSE, накопленном результате
stream и синтетическом SSE. Semantic cache отключён при `logprobs=true`, поскольку
вероятности относятся к точному контексту. Exact cache включает все controls в
ключ. Reasoning tokens входят в общий completion usage, а не прибавляются повторно.

Контракт: [Chat API reference](https://developers.openai.com/api/reference/python/resources/chat/subresources/completions/methods/create).


Anthropic adapter преобразует chat `stop` (строка или массив до четырёх строк)
в native `stop_sequences`. `parallel_tool_calls` для Chat и Responses передаётся
как инвертированный `tool_choice.disable_parallel_tool_use`, в том числе при
forced tool choice и structured output. При отсутствии tools параметр не создаёт
искусственного tool choice. Проверено для обычных и streaming wire requests.
Native семантика: [parallel tool use](https://platform.claude.com/docs/en/agents-and-tools/tool-use/parallel-tool-use).

Когда adapter не поддерживает native streaming, Chat SSE формируется из одного
обычного provider response. Этот fallback сохраняет tool calls, их аргументы и
служебные метаданные, а также возвращает итоговый usage отдельным событием с
пустым `choices` перед `[DONE]`. Индексы tool calls назначаются для SSE без
изменения исходного ответа. Повторного inference или отдельного списания нет.

OpenAI-compatible chat stream допускает индексы choices и tool calls от 0 до 127.
Отрицательные и выходящие за пределы индексы upstream возвращают ошибку до
выделения массивов и передачи некорректного события клиенту. Это ограничение
накопления stream, а не объявление поддержки параметра `n` в публичном API.

Managed deployment с capability `stream` включает native streaming в adapter.
Без этой capability создание adapter не включает native stream неявно. Настройка
применяется при построении runtime endpoint из сохранённого deployment.


## Native Gemini adapter

Provider type `gemini` uses the GenerateContent protocol, not an OpenAI-compatible
URL. Set `base_url` to `https://generativelanguage.googleapis.com` (or an explicit
`/v1beta` or `/v1` base) and use a provider-scoped API-key credential. The key is
sent only in `x-goog-api-key`; redirects are not followed. Managed deployments
must explicitly include `stream`, `tools`, `vision` or `structured_output` when
those capabilities are needed.

Chat supports system/developer instructions, text and inline image parts, function
tools/results, forced tool choice, JSON output schemas, temperature, top-p, seed,
stop sequences and output token limits. Opaque function-call signatures round-trip
in `tool_calls[].extra_content.google.thought_signature`; Anthropic/Ollama/demo
reject this metadata instead of silently dropping it. Model-generated thinking
text is excluded from the chat content, while thinking tokens are included once
in completion usage. Reported prompt/cache/total usage is retained for billing.

Native SSE accumulates content and tool calls, forwards translated events and
requires a completed finish reason. It rejects invalid/duplicate candidate
indices, invalid usage, truncated streams and oversized payloads. JSON responses
are limited to 32 MiB and the accumulated SSE wire payload to 64 MiB.

Discovery follows native pagination with a 30-second overall deadline, a maximum
of 100 pages/10,000 scanned models and repeated-token detection. Only models
advertising `generateContent` are offered. Native Responses, embeddings, inbound
GenerateContent/Interactions and cloud workload identity remain separate gaps.
Unsupported generation controls, parallel tool control and strict function
schemas fail explicitly; seed/output limits must fit the native integer range.

Protocol references: [GenerateContent](https://ai.google.dev/api/generate-content)
and [tool signatures](https://ai.google.dev/gemini-api/docs/generate-content/thought-signatures).

## Native Anthropic model discovery

Managed Anthropic discovery follows `has_more` and `last_id` using `after_id`
instead of returning only the first page. It uses the selected provider-scoped
credential in `x-api-key` and the native version header. Redirects are rejected.
The result is sorted and deduplicated only after all pages succeed; a later-page
failure returns an error rather than a partial catalog.

Discovery has a 30-second overall deadline, a 10-second request timeout, a 2 MiB
page limit, and limits of 100 pages and 10,000 scanned records (including
duplicates). Invalid continuation cursors, empty continuing pages and pagination
cycles fail explicitly. Caller cancellation also stops the current request.
This does not publish the inbound Messages API or automatically change pricing.
Protocol: [Anthropic Models API](https://platform.claude.com/docs/en/api/models/list).


## Inbound Messages API

`POST /v1/messages` accepts a gateway virtual key in `x-api-key` or the existing
Bearer header and requires `anthropic-version: 2023-06-01`. Conflicting keys and
beta headers are rejected. The model is a gateway model/alias. Requests enter the
same Chat authorization, tool ACL, TPM/RPM, routing, content policy and billing
pipeline; this endpoint does not forward client credentials to providers.

Supported input is text, text system blocks, base64 user images, function schemas,
assistant tool-use history, text tool results, tool choice and parallel-tool
control, temperature, top-p, positive max_tokens, stop_sequences and stream. Provider-specific
capability checks still apply after conversion. Responses contain native text or
tool-use blocks and native usage fields. Cached prompt tokens are separated from
uncached input tokens without changing the internal accounting totals.

SSE emits message_start, content_block_start/delta/stop, message_delta and
message_stop. Function argument fragments use input_json_delta. Errors after
stream start emit an error event without successful completion. Native streaming
and the ordinary-response fallback both use the same output conversion. The
conversion buffers at most 32 MiB per JSON response or unfinished SSE frame and
at most 128 concurrent tool identities. Accumulated function arguments have a
separate 32 MiB total limit and must form JSON objects before completion. A
stream without a finish reason fails.

Compatibility is partial. Unsupported top-level fields and block fields fail
with a native invalid_request_error. In particular, thinking, cache controls,
server tools, documents, URL images, metadata, top_k, assistant
prefill, text after tool_use and is_error=true tool results are not supported.
All tool-use history requires matching results. Opaque provider tool metadata
that cannot be represented in Messages produces an explicit conversion error.
Token counting is a separate endpoint; background jobs are not supported.

Regressions cover request/response conversion, native and fallback SSE, stream
failure, model/tool authorization, TPM, unknown input, response-size bounds and
reported usage reaching the accounting stage through Router. No live paid
provider calls were used. Protocol references:
[Messages](https://platform.claude.com/docs/en/api/messages/create) and
[streaming](https://platform.claude.com/docs/en/build-with-claude/streaming).

When an adapter reports an exact matched stop sequence, Chat choices preserve it
in the optional `stop_sequence` result field. Anthropic populates this only for
its native `stop_sequence` reason; `end_turn` does not invent a match. JSON,
stream accumulation and fallback SSE retain the value without trimming it.
Messages then reports native `stop_reason: stop_sequence` with the exact value.
This is an additive response-contract extension.

Inbound Messages `stop_sequences` accepts up to four non-empty delimiters. A
non-empty list requires an adapter that reports exact matched stops (currently
native Anthropic). The router rejects unsupported adapters before provider
modules, billing reserve and cache lookup, including fallback attempts. Empty
lists impose no additional requirement. An internal Go request flag carries this
constraint; it is neither accepted as client JSON nor forwarded upstream.

Requests requiring exact stop metadata use separate exact-cache keys and bypass
semantic cache. A native stop-sequence reason without its matched delimiter is
an upstream error rather than an inferred end_turn. JSON/SSE regressions cover
native conversion, cache isolation and rejection before accounting modules.


## Native token counter adapter

`TokenCountClient` is an optional adapter interface. Anthropic uses native
`/v1/messages/count_tokens` for model context, system instructions, text/inline
images, function schemas, function-call history/results and tool choice. The
counter wire request contains no generation limit or streaming flag. Unsupported
context parts and parameters fail before HTTP. Results retain the provider model
and source; errors never fall back silently to the local context estimate.

The adapter enforces a 30-second context deadline, the inference body limit and
a 64 KiB response limit. Redirects are refused to protect the provider API key;
missing, negative, fractional or overflowing counts are errors. Caller
cancellation is propagated. The counter is not a replacement for reserve estimates.

`POST /v1/messages/count_tokens` exposes the counter with the same native auth
headers as Messages. It accepts model, messages, system, tools and tool_choice;
generation parameters are rejected. Partial assistant/tool-use history is allowed
for counting. Advanced block types remain unsupported.

The endpoint applies gateway auth, model/tool ACL, access groups and shared
RPM/input TPM admission. TPM uses the local input estimate before contacting the
provider; the returned provider count does not generate a billing event. Router
runs configured pre-inference policies (including anonymization, DLP and AV),
excluding the billing module, then uses deployment admission limits. Thus the
reported count describes the context after those policies, including model alias
resolution. No generation post/failure lifecycle, reserve, mirror, retry or
response cache runs. Counter errors never become successful estimated counts.
The Router operation has a 30-second deadline. Unsupported selected adapters fail
explicitly before provider modules. Observer metrics use operation count_tokens.
Protocol: [native token counting](https://platform.claude.com/docs/en/api/messages/count_tokens).


Gemini also implements `TokenCountClient` and is selectable through the same
`/v1/messages/count_tokens` gateway route. It calls native `models.countTokens`
with `generateContentRequest`, including system instructions and function schemas
as well as message contents and inline images. The nested model is the resolved
provider model. Only x-goog-api-key carries the provider credential; no key is
placed in the URL. Results use totalTokens as the counted input, including cached
context, without creating generation usage or altering reserve estimation.

The same 30-second deadline, inference request-body limit, 64 KiB response limit,
redirect refusal and invalid-count checks apply. Gemini-specific unsupported
controls still fail before HTTP. Tests cover complete native context, model alias
routing, malformed counts, redirect refusal and cancellation. This does not add
inbound GenerateContent or cloud workload credentials.
Protocol: [Gemini token counting](https://ai.google.dev/api/tokens).

## GenerateContent request conversion

The internal GenerateContent converter maps native system/contents, inline user
images, function declarations and results, tool choice, output limits,
temperature/top-p/seed, stop sequences and JSON output configuration to Chat.
Native Schema types are normalized to JSON Schema for the supported subset;
unknown native schema fields require using parametersJsonSchema or
responseJsonSchema instead. Multiple candidates and unsupported native fields
are rejected, so output reservation is not silently multiplied.

Function-call signatures are preserved. Missing call IDs receive deterministic
request-local IDs; response references must match a pending call. An ambiguous
name-only response is rejected. Native function-response objects are encoded as
JSON tool content for the shared pipeline. The Gemini adapter now sends object
results, including strings containing JSON objects, directly as native response
objects. Plain text and non-object JSON keep the result wrapper. This changes the
native wire representation of object-valued tool results to avoid double wrapping.

Regression tests cover native context/config conversion and function history
round-trips through the adapter. This increment is conversion groundwork; no
inbound GenerateContent HTTP route is exposed yet.
