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
| `/v1/responses` | `top_logprobs`, `truncation`, `reasoning`, `store`, `include`, `provider`, `model`, `input`, `instructions`, `tools`, `tool_choice`, `parallel_tool_calls`, `text`, `previous_response_id`, `stream`, `max_output_tokens`, `max_tokens`, `temperature`, `top_p` |
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
| `gemini` | Native GenerateContent chat/stream, tools, inline vision, structured output, text embeddings; API key |
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
advertising `generateContent`, `embedContent` or `batchEmbedContents` are offered.
Native Responses, advanced inbound GenerateContent options, Interactions and cloud
workload identity remain separate gaps.
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
cloud workload credentials.
Protocol: [Gemini token counting](https://ai.google.dev/api/tokens).

## Native GenerateContent API

POST `/v1beta/models/{model}:generateContent` returns native JSON; POST
`/v1beta/models/{model}:streamGenerateContent?alt=sse` returns native SSE.
Send a gateway key in `x-goog-api-key` or Bearer authorization. Query credentials
and conflicting authentication headers are rejected. Both endpoints use the shared
Chat pipeline for authorization, model/tool ACL, quotas, content policy and billing.

The GenerateContent converter maps native system/contents, inline user
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

Responses preserve function-call signatures and report reasoning tokens separately
from candidate tokens, while billing includes both. The Gemini adapter preserves
upstream modelVersion when provided; otherwise the normalized model name is used.
SSE emits text incrementally and buffers function calls until their arguments are
complete JSON objects. Frame and tool accumulation have separate 32 MiB limits.
Stream failures emit a redacted native error without a successful finish reason.

Regression tests cover conversion, JSON/SSE, authorization, quotas, native usage
and billing, bounded stream accumulation and disconnects. Advanced safety, grounding,
thought output, file/audio parts and Interactions remain unsupported.


### Native GenerateContent token counting

POST `/v1beta/models/{model}:countTokens` accepts either `contents` or
`generateContentRequest` with contents, systemInstruction, tools and toolConfig.
An optional nested model must match the path model (the `models/` prefix is accepted).
The two input forms are mutually exclusive. Generation options, cachedContent and
unsupported native parts are rejected. Authentication uses the same gateway headers
as GenerateContent; only optional `alt=json` is allowed in the query.

The response contains provider-reported `totalTokens`. The endpoint shares the
Messages counter's model/tool ACL, input TPM/RPM and pre-inference policy checks.
Quota windows are shared across protocols. Counting does not open a generation
billing lifecycle or use generation cache/retries; unsupported providers return an
error instead of an estimated count. Native counters currently cover Gemini and
Anthropic. Modality-specific breakdowns are not synthesized.

Regression tests verify native system/tools delivery, alias routing, validation,
authorization, shared quotas and absence of generation billing.
Protocol reference: [Gemini token counting](https://ai.google.dev/api/tokens).

### Admission after gateway modules

Model/tool authorization, token reservation and model policy attachment use the
request produced by the gateway pre-inference pipeline. Replacing a request or
its context in a module does not leave admission checking an earlier copy. This
applies to Chat (including native generation protocols), Responses, Embeddings,
Rerank and both native token counters. A module that removes a typed inference
request causes a 502 module error before provider execution.

Regression tests cover rewritten models, added tools, expanded input context and
replacement/removal of typed requests. Provider-specific modules still run in
their existing provider execution phase.


Native Gemini function calls may omit `args`. The inbound history converter and
outbound JSON/SSE adapter normalize omitted or null arguments to `{}`, preserving
call IDs and thought signatures. Array, scalar and malformed arguments remain
errors. This allows parameterless tools to complete a generation/history round trip.
See [FunctionCall](https://ai.google.dev/api/generate-content#FunctionCall).


### Native Schema constraints

GenerateContent tool parameters and responseSchema support `nullable`, `anyOf`,
array/string/object size bounds, `pattern`, numeric bounds, enum and required
properties, plus title/description/format/default/example annotations. Native
`example` becomes JSON Schema `examples`; nullable wraps the complete schema in
an anyOf with null, so enum and other constraints remain effective for non-null values.

Size bounds accept native nonnegative int64 strings or exact JSON integers.
String-encoded int64 bounds are emitted as JSON numbers without float rounding;
JSON numeric bounds above 2^53-1 must be supplied as strings. Contradictory bounds,
invalid field types and unknown constraints are rejected. Native Schema traversal
is limited to 64 levels and 128 alternatives per anyOf. propertyOrdering remains
unsupported; callers can use the explicit JSON Schema fields when appropriate.

Regression tests validate accepted/rejected instances against converted schemas,
including nullable enums, nested alternatives and exact int64 serialization.


### Native Gemini embeddings

Provider type `gemini` supports `/v1/embeddings` through synchronous
`batchEmbedContents`. Configure an embedding model alias and the `embeddings`
capability on its deployment; discovery now includes embedding models. A request
contains one string or up to 100 strings and produces vectors in the same order.
Optional `dimensions` is forwarded as outputDimensionality. The adapter requires
float output and rejects user metadata and token-ID inputs.

Requests use API-key headers, a 30-second deadline and no redirects. Responses
are limited to 32 MiB and must contain exactly one nonempty vector per input,
consistent dimensions (at most 65536), and the requested dimension when set.
Provider/model limits may be stricter. Native usageMetadata.promptTokenCount is
used when present, including zero. If usage is absent, the gateway's existing
context estimator supplies usage; this is not model-specific tokenization.
Malformed usage is rejected rather than replaced with an estimate.

The internal EmbeddingResponse type has a non-JSON UsageReported flag so a reported
zero survives usage merging. The public response schema is unchanged. Native
adapter and endpoint tests cover vector order, alias routing, credentials, quotas,
parameter rejection before billing, reported usage, response bounds and redirects.
Task-specific embedding options, multimodal input and asynchronous batches remain
separate gaps. [Native embedding protocol](https://ai.google.dev/api/embeddings).

OpenAI-compatible and Ollama embeddings also preserve provider-reported zero
usage. Only absent usage falls back to the pipeline estimate. OpenAI-compatible
usage objects must include nonnegative prompt_tokens and total_tokens, with total
at least prompt and no completion tokens; malformed objects now return an error.
Ollama rejects negative prompt_eval_count. This is a stricter upstream response
validation rule; the public request/response schema is unchanged. Regression tests
exercise missing, zero, positive and malformed counts for both adapters.

OpenAI-compatible and Ollama embedding responses are limited to 32 MiB before
JSON decoding. Trailing JSON, truncated payloads and read errors fail explicitly.
Responses must have one nonempty float vector per input, unique indices in the
input range, consistent dimensions no greater than 65536, and the requested
`dimensions` when provided. Valid out-of-order indexed vectors retain their order.
This tightens acceptance of malformed or oversized upstream responses; valid
responses below these limits retain their existing public representation.
Regression tests verify bounded reads and adapter-level rejection, plus vector
count/index/dimension checks.

### Capability intersection

An explicit deployment capability list is an upper bound even when the model
catalog advertises more operations. Routing and shadow selection require both
the deployment and catalog to permit the requested capabilities. Legacy empty
lists retain their existing catalog-driven behavior and explicit opt-in rules.
Deployments that previously relied on the catalog overriding their nonempty list
must include every intended operation in that list.

The native Gemini adapter declares that Responses is unsupported. Such endpoints
are excluded before selecting a Responses route, allowing another compatible
endpoint to serve the request. Direct calls still return the explicit unsupported
protocol error. The optional internal SupportsResponses capability hook preserves
behavior for existing adapters that do not implement it.

Regression tests reproduce catalog expansion of a chat-only deployment and an
unsupported native endpoint blocking a valid Responses route, then verify the
corrected selection behavior.

### Responses affinity storage failures

When a continuation has previous_response_id and its configured affinity store
fails to read, the gateway returns HTTP 503 (`response_affinity_unavailable`)
before provider-specific modules or upstream calls. Streaming requests fail before
opening SSE and do not retry the lookup through the JSON fallback path. The error
response does not expose the storage error text.

This deliberately changes the previous behavior that routed despite a lookup
failure. Successful lookups still re-evaluate endpoint/model capabilities; a
missing or expired binding retains existing cache-miss behavior. First requests
without previous_response_id do not need a lookup. Failure to persist a new binding
retains the existing logged best-effort behavior and is a separate durability gap.
Regression tests cover JSON and streaming lookup failures, no provider execution,
no provider-module execution, and preservation of the existing pinned non-streaming
fallback tests.

### Responses cache endpoint ownership

Responses exact-cache entries include the selected deployment name, provider ID,
provider type, and actual upstream model in addition to the existing credential,
user, and effective-policy scope. Response IDs refer to upstream state and cannot
be reused across deployments, even when they expose the same logical model.
Changing an alias target also invalidates its Responses entries. Existing entries
use a different key shape and expire naturally; no cache deletion is required.
Chat cache keys are unchanged.

A Responses cache hit refreshes the response's affinity binding to its owning
endpoint before post-response modules, preserving continuation routing when the
binding has expired before the cached response. The hit keeps zero generation
usage and runs the normal post-response pipeline without another upstream call.
This does not extend provider-side response retention, or make failed affinity
writes durable. Repointing an existing deployment name to a different provider
account still requires a cache/affinity namespace or retention transition.

Regression tests cover two deployments serving the same request, same-endpoint
cache reuse, changed alias targets, expiry followed by cache hit and continuation,
and affinity ordering before the cache-hit billing callback.

### Monitoring Responses affinity

`ai_gateway_cache_operations_total` includes `operation="affinity_get"` with
`result="hit|miss|error"` and `operation="affinity_set"` with `result="ok|error"`.
Only actual store calls are counted, including writes that refresh cache hits.
These operations also appear in authenticated cache diagnostics; they do not
change exact/semantic cache hit ratios. Labels contain no response IDs, user IDs,
credential IDs, endpoint names, or storage error text.

Alert on increases of
`ai_gateway_cache_operations_total{operation="affinity_set",result="error"}`:
a completed generation may have lost its continuation binding. The gateway logs
a fixed message, retains the successful response, and runs post-response billing
with the reported usage. This is observable best-effort persistence, not durable
session storage. Read errors instead fail closed with HTTP 503. Regression tests
cover JSON and streaming write failures, original usage reaching billing once,
zero-usage cache hits, and read hit/miss/error counters.

### Responses continuation fallback policy

When affinity resolves `previous_response_id` to an eligible endpoint, that
endpoint is the only execution candidate. Its failure does not activate general,
context-window, or content-policy model-group fallbacks: authorization to use
another model does not prove that it owns the previous response's upstream state.
A pinned endpoint that was originally selected through fallback is also included
only once. Configured retries on the same endpoint retain their existing policy.

This intentionally tightens continuation behavior. Initial requests without a
previous response retain normal model-group fallback; a successful initial
fallback still establishes affinity to its selected endpoint. Missing or expired
affinity retains the documented cache-miss behavior. Native streaming and JSON
share this candidate restriction, and pinned endpoints without native streaming
retain the existing JSON fallback to the same endpoint.

Regression tests cover JSON and streaming with primary/fallback endpoint pins
across unavailable, context-length, and content-policy errors. They assert the
original error is preserved and no other endpoint executes. Existing tests also
verify initial fallback followed by a successful pinned continuation.

### Responses shadow traffic and continuity

Requests with `previous_response_id` are not mirrored to shadow deployments.
The shadow provider does not own the primary response's state, and the gateway
has no mapping from primary response IDs to an independently executed shadow
conversation. This rule applies to JSON and streaming requests regardless of
whether affinity storage is enabled or its binding is present.

Independent Responses requests retain configured shadow sampling, model alias
conversion, and bounded asynchronous execution. Continuations still execute on
their normal primary route and run its billing pipeline. This changes only
shadow traffic; no conversation history is reconstructed or copied as a fallback.
Regression tests synchronize asynchronous execution with the standard Go
`testing/synctest` package and verify initial requests are mirrored, continuations
are skipped, and primary usage reaches billing exactly once for JSON and SSE.

### Chat streaming without a native streaming deployment

When no eligible deployment provides native Chat streaming, the gateway proceeds
to its ordinary non-streaming Chat route and emits synthetic SSE after a successful
JSON response. This includes deployments whose explicit capability list contains
`chat` but omits `stream`. The normal route still checks model/deployment
capabilities and applies provider modules, limits, cache, and billing. It sends
`stream=false` upstream and does not provide live incremental latency.

A native streaming attempt that returns an error remains terminal to the handler;
this fallback is not an extra retry after a failed stream. If no non-streaming Chat
route is eligible, the ordinary routing error is returned without opening SSE.
Regression tests cover a JSON-only deployment, one upstream call on failure, and
rejection of a deployment that lacks the Chat capability.

### Synthetic Responses SSE

When no native Responses streaming route is available, `stream=true` uses the
ordinary JSON Responses pipeline and replays its result as named SSE events.
This fixes the former JSON response to a streaming request. Authorization,
capability checks, affinity, provider modules, and billing run before replay;
there is no second generation. Native stream errors do not trigger JSON retries.

Replay includes response creation, ordered output-item/content events, complete
text deltas, function-call argument deltas, and the terminal response with usage.
Events have increasing sequence numbers. Existing item IDs are preserved; missing
IDs receive response-scoped IDs, and output_text-only results get a message item.
The final status is preserved for completed, incomplete, and failed results;
non-terminal results fail before SSE begins. A write failure stops replay without
emitting a false completion. Events follow the
[Responses event contract](https://developers.openai.com/api/reference/typescript/resources/beta/subresources/responses/methods/create).

This is buffered replay, not live token streaming or background job support.
Payloads remain limited to the gateway's existing response types. Regression tests
cover JSON-only deployments, upstream errors, capability denial, text and function
output ordering, usage, terminal statuses, and client write errors.

### Responses outcome details

The response contract retains provider `error.code`, `error.message`,
`incomplete_details.reason`, and content-part `refusal` text. These are additive
optional fields in the public Go types and JSON/OpenAPI response schema. Previously
they were discarded by typed JSON decoding, leaving an unexplained failed or
incomplete result, or an empty refusal.

Synthetic SSE preserves outcome details in its terminal response and emits
`response.refusal.delta` / `response.refusal.done` for refusal content. Creation
and empty content-part events do not expose the future terminal details or refusal
text. Refusal content follows the existing deanonymization behavior. This does
not implement retries, background execution, or provider-specific error handling.
Regression tests cover JSON round trips, terminal SSE details, refusal events,
and restoration of anonymized refusal text. Event fields follow the
[Responses streaming reference](https://developers.openai.com/api/reference/resources/responses/streaming-events).

### Retry-After numeric bounds

Provider retry headers are checked for a positive, finite, representable duration
before integer conversion. Invalid or overflowing `Retry-After-Ms` values no
longer suppress a valid `Retry-After` fallback. Existing fractional-unit support,
HTTP-date parsing and scheduler wait limits remain in effect. Deterministic tests
cover large values, infinity and NaN without sleeping or calling external services.

Provider HTTP error normalization accepts a bounded identifier from `error.type`
when `error.code` is absent or blank, supporting native error envelopes. An
explicit code keeps precedence. The same safe identifier filter applies to both
fields; raw messages remain excluded. Tests cover native types, precedence,
whitespace, overlong/unsafe values and preservation of HTTP-based classification.

Numeric `error.code` values no longer invalidate the error envelope. For envelopes
with a numeric code, a safe `error.status` can supply the diagnostic identifier.
Precedence is string code, type, then status; actual HTTP status still drives the
base failure classification. Regression tests cover numeric codes, status/type
precedence and filtering of unsafe status text. Numeric body codes are not used
to override the HTTP status or retry classification.

### Responses stream output-index bound

Native Responses SSE decoding accepts explicit `output_index` values only as
integers from 0 through 1023. Validation occurs before integer conversion, slice
expansion, or forwarding the event. Negative, fractional, nonnumeric, null, and
oversized indices now fail with an upstream protocol error instead of silently
using or allocating an output slot. An omitted index keeps the legacy slot-zero
behavior; sparse indices within the bound remain supported.

This intentionally limits stream output slots to 1024. It bounds allocation driven
by an index, not total response bytes, text accumulation, or background lifecycle.
Regression tests cover malformed values, numbers beyond machine integer range,
the first rejected index, the highest accepted index, and omitted indices.

### Native Responses refusal assembly

Native SSE accumulation resolves the event name from JSON `type` when the SSE
`event:` line is absent. Only `response.output_text.delta` contributes to ordinary
text; function arguments and refusal text are collected separately. Other delta
events continue to be forwarded, but are no longer misrepresented as answer text.

Refusal delta/done events retain output/content indices and item identity. The
final refusal replaces accumulated fragments rather than duplicating them.
Explicit refusal `content_index` is limited to integers 0–127 and is validated
before allocation or forwarding; omitted values retain slot-zero compatibility.
These limits do not bound cumulative text bytes. Regression tests cover named and
JSON-typed events, interleaved text/refusal/tool output, unrelated deltas, refusal
completion, invalid indices, and the highest valid content slot.

### Native Responses SSE byte budget

Native Responses SSE decoding now limits consumed wire data to 32 MiB per
stream, including comments, event headers, multiline data, and unfinished frames.
The decoder reads at most one byte beyond the boundary to distinguish an exact
fit from an oversized stream. Exceeding it returns an explicit upstream protocol
error rather than treating the boundary as successful EOF. Existing line-size
limits still apply independently.

This bounds stream-driven text/argument accumulation and frame buffering by the
input budget; it is not a precise process RSS bound or a global concurrency limit.
A response already partially sent to the client can end with an error. The limit
does not change JSON response limits. The shared SSE scanner also discards an
unfinished frame on a source I/O error instead of flushing it before reporting
the error; this applies to all adapters using that scanner. Regression
tests cover an oversized stream made of short lines, exact reader boundaries,
limited upstream reads, terminal errors, and preservation of source I/O errors.

### Responses JSON body bound

The OpenAI-compatible Responses adapter accepts one non-null JSON object of at
most 32 MiB, including surrounding whitespace. It reads no more than the limit
plus one byte, rejects oversized bodies, trailing documents/data and null, and
preserves source I/O errors. A valid response exactly at the boundary remains
accepted with its output and usage intact. This tightens acceptance of malformed
upstream bodies; unknown fields inside an otherwise valid object retain the
existing decoding behavior.

Regression tests exercise the actual HTTP adapter with invalid documents and an
oversized body, plus exact-size, read-limit, and I/O-error cases. The limit applies
to Responses JSON only; it does not change Chat, embedding, or native adapter
contracts, and is separate from the native Responses SSE wire budget.

### Responses usage range validation

The OpenAI-compatible Responses JSON and native SSE decoders reject negative
input, output, total, cached, cache-write, and cache-creation token counters.
They also reject input/output values whose sum exceeds the platform integer
range, using subtraction before any addition. SSE validation happens before the
containing response event is forwarded; JSON validation happens before the
adapter returns a successful result to provider post-response modules.

This preserves the existing treatment of absent or partial usage, and does not
assert that every provider's total equals the input/output sum. Usage estimation
and missing-versus-explicit-zero handling are separate concerns. Regression tests
cover both decoders, invalid cache detail counters, overflowing sums, exact integer
boundaries, and compatible missing/partial usage.

### Reported zero versus missing Responses input usage

The OpenAI-compatible JSON and native SSE decoders retain an internal
`InputTokensReported` flag when `usage.input_tokens` is present and non-null,
including zero. Provider post-processing no longer substitutes an estimate for
that zero or increases total_tokens because of it. Missing/null usage and
output-only usage retain the previous estimation behavior. Partial SSE response
snapshots retain a count already reported earlier in the same stream.

The Go response type gains an additive internal field excluded from JSON; clients
see no new wire field. Other native adapters keep their existing presence behavior.
Regression tests cover missing, null, output-only, zero and positive usage in both
JSON and SSE, partial snapshots, and exclusion of the internal marker from JSON.

### Exact integer usage in native Responses SSE

Native Responses events retain JSON numbers during intermediate decoding instead
of converting them to float64. Typed token counters therefore keep exact integer
values above 2^53 and through the platform integer maximum, matching JSON-response
decoding. Existing integer-range and negative-usage validation still applies.
This fixes one-token rounding and rejection of otherwise representable counters.

Only bounded output/content indices are converted to floating point for the
existing small-range index validation. Events must still contain a single JSON
value; trailing documents or junk are rejected before forwarding. Regression tests
compare JSON and SSE counters at precision boundaries and preserve trailing-data
rejection and integer-valued index representations.

### Responses cache outcome policy

Responses exact-cache writes and reads now require `status="completed"` (or the
legacy omitted status) and no `error` or `incomplete_details`. Failed, incomplete,
cancelled, queued, and in-progress outcomes are not reusable cache results, even
when the upstream HTTP request itself succeeded. Contradictory completed results
with error/incomplete details are also excluded.

Previously saved outcomes that do not satisfy this policy are ignored on read;
they expire normally without destructive cache cleanup. The request executes the
ordinary provider path and its reported usage reaches post-response billing.
Successful cache hits retain zero generation usage. This policy does not add
automatic retries or asynchronous job polling.

Regression tests cover fresh and pre-seeded entries for every listed state,
legacy status compatibility, repeated upstream calls for non-cacheable outcomes,
and billing callbacks for both real execution and successful cache hits.

### Safe Responses usage estimation

Merging a missing input-token count with the local prompt estimate now checks
nonnegative counters and remaining integer capacity before mutation. Both the
resulting total and input/output sum must fit. Explicit provider input counts
(including reported zero) still take precedence over the estimate.

On invalid usage, JSON and streaming router paths stop before successful
post-response billing. The JSON path also validates before writing cache data.
The failure lifecycle runs once, and the router does not retry the generation.
The provider may already have executed; this prevents corrupted counters from
being committed, rather than guaranteeing reconciliation of unusable upstream
usage. Regression tests verify no cache entry or successful billing callback,
one provider call and failure callback, exact boundaries, and unchanged counters
when a merge is rejected.

### Native Responses SSE completion requirement

A native Responses stream must contain `response.completed`, `response.incomplete`
or `response.failed` with a response object before EOF or `[DONE]`. An explicit
status must agree with the event; an omitted status is inferred from it. Empty,
created-only, and partial-output streams now return an unexpected-EOF error rather
than a fabricated successful response. A valid terminal event followed by clean
EOF remains accepted without requiring the optional `[DONE]` sentinel.

This tightens the shared native Responses decoder used by OpenAI-compatible and
Ollama Responses adapters. On an interrupted result, the router runs its failure
lifecycle instead of a successful post-response billing callback. Already emitted
partial output cannot be recalled. HTTP regression tests cover valid terminal
states, missing/contradictory outcome payloads, truncation, and billing/failure
callbacks. Collector fixtures now contain explicit terminal outcomes.

### Responses text part assembly

Native Responses SSE text deltas are assembled by `output_index` and
`content_index`, preserving each message's item ID. Text `done` events replace
their part's accumulated text. Content indices use the same bounded validation
as refusals; invalid indices fail before forwarding the offending event.

The derived `output_text` concatenates all text parts in output/content order,
excluding refusals and tool arguments. This also applies to decoded JSON
Responses. A final response output snapshot replaces previously assembled text,
including when the snapshot contains an empty output array. Top-level text is
still accepted as a fallback when no nonempty structured text is available.

Regression tests cover interleaved messages and parts, text completion events,
empty and populated terminal snapshots, invalid content indices and JSON text
aggregation. The tests also reproduced the earlier behavior through a temporary
Go overlay before passing with the fix.

Native Responses function-call argument events likewise preserve `item_id` and
assemble arguments separately by output index. A
`response.function_call_arguments.done` event replaces partial arguments with
the reported final string, including when no delta events preceded it. Function
items do not retain the decoder's initial message content or assistant role.
Regression tests cover interleaved calls, preserved call metadata, both SSE event
name forms and a final argument event without deltas.

### Responses terminal event boundary

After validating and forwarding the first terminal Responses event, the native
decoder returns its result immediately. Later frames cannot replace the response
ID, status, text or usage passed to post-response accounting. The decoder does
not wait for upstream EOF or an optional `[DONE]` after that outcome. A failure
writing the terminal event is still returned to the caller.

Regression tests cover all three terminal states, duplicate outcomes with changed
usage, late text, malformed trailing frames, read failure after the terminal frame
and terminal writer failure. Upstream streams without a valid terminal outcome
continue to fail as documented above.

Output-item snapshots replace the accumulated item rather than merging into its
existing fields. This prevents obsolete message content, role or status from
surviving in a function-call snapshot. The derived top-level text is invalidated
and rebuilt from the current output. Regression coverage exercises both added
and done item snapshots, including a previously populated top-level text field.

The native decoder also consumes `response.content_part.added` and
`response.content_part.done` snapshots. Each snapshot replaces its indexed
content part and preserves the message item ID, so final text and refusal content
remain available even without individual deltas. Output and content index bounds
apply before allocation. Missing, non-object or malformed typed parts fail before
event forwarding. Tests cover both events, multiple content slots, replacement of
earlier text by a refusal and invalid payloads.

### Native Responses reasoning summaries

The SSE collector preserves `response.reasoning_summary_part.added/done` and
`response.reasoning_summary_text.delta/done` in each reasoning item's `summary`.
Parts are indexed by `summary_index` (0 through 127) within the bounded output
item array. Final text and part events replace accumulated fragments; deltas
append to their selected part. Summary text remains separate from `output_text`.
Invalid part/text payloads and summary indices fail before forwarding the event.

Regression tests cover part snapshots, delta/final text replacement, multiple
summary parts alongside a normal answer, and invalid indices for all four event
types. The refusal isolation fixture now gives reasoning its own output index.
Event fields follow the [Responses streaming reference](https://developers.openai.com/api/reference/resources/responses/streaming-events).

JSON-to-SSE fallback emits the same four summary events for each reasoning
summary part before completing its output item. Empty text is explicitly included
in added and completed summary parts. This is a replay of the existing JSON
result after the normal pipeline, without another provider execution. Regression
coverage checks event order, sequence numbers, indices, empty text, unchanged input
data and immediate termination on writer failure at each summary event.

Responses output items preserve optional `encrypted_content` as an opaque string.
This additive response-schema field survives JSON decoding, native SSE item and
terminal snapshots, and synthetic SSE item/completion events. The gateway does
not decrypt or rewrite it. Regression tests reproduce its former loss in each of
these paths. Absent or null values remain omitted on serialization; an explicitly
empty string remains present. This change preserves returned context; it does not
add a response-storage or background-job lifecycle API.

The Responses request contract now accepts optional `include` string arrays.
OpenAI-compatible and Ollama Responses adapters forward them in both JSON and
streaming requests; supported values remain an upstream capability. This enables
clients to request `reasoning.encrypted_content` where the upstream supports it.
Anthropic and Demo reject nonempty `include` with `unsupported_parameter` instead
of silently dropping the option. Empty or omitted arrays preserve prior behavior.
Local HTTP regression tests check the actual upstream payload in all four
forwarding paths and adapter rejection. The public OpenAPI schema includes the
new optional request field.

Responses also accepts optional boolean `store`. OpenAI-compatible and Ollama
forward explicit `true` and `false` in JSON and SSE requests; an absent or null
value leaves the upstream default in effect. Anthropic and Demo reject either
explicit value with `unsupported_parameter` because these adapters cannot express
the requested Responses storage control. This is an additive request-schema
change, covered by local HTTP payload tests for both values and default behavior.

`store` controls upstream response storage only. Gateway cache and logging policies
are configured separately; this option is not a gateway-wide retention switch.
It does not publish retrieve/delete/cancel endpoints or a background lifecycle.

Opaque `encrypted_content` fields are protected during input text processing.
Local text transformation leaves them unchanged, the remote text-only projection
omits them, and projection merging retains the original value even if a processor
returns a replacement. Reasoning summary text remains transformable. Regression
tests cover transformation, projection, hostile replacement, original-input
preservation and text restoration. This prevents text anonymization from
corrupting provider-encrypted continuation context.

An HTTP-handler regression now exercises two stateless turns in JSON, native SSE
and JSON-to-SSE fallback modes. The client reuses the returned reasoning item as
input alongside new user text. A local fake upstream verifies unchanged opaque
context, `store=false`, `include`, and anonymized user email. The post-response
recorder verifies two usage callbacks with reported totals and distinct internal
execution IDs despite identical external request IDs. This verifies gateway
transport and pipeline integration; it does not exercise real provider encryption,
remote authorization or durable PostgreSQL billing.

Anthropic Responses conversion rejects reasoning/compaction input items and
items carrying `encrypted_content` with `400 unsupported_parameter`, `param=input`.
These provider-specific items cannot be represented by the adapter's Messages
conversion. Validation runs before upstream HTTP execution in both JSON and SSE
paths. Regression tests verify zero upstream calls for those inputs and continued
acceptance of ordinary message input. Native Responses forwarding remains the
path for upstreams that understand their own encrypted continuation context.

### Anthropic Responses tool history

Responses `function_call` history items now become assistant `tool_use` blocks,
and `function_call_output` items become user `tool_result` blocks linked by
`call_id`. Consecutive calls or results of the same role are grouped into a single
message. Arguments must be a JSON object and retain exact JSON numeric values;
missing identifiers, names, invalid arguments or invalid output types fail with
`400 invalid_request`, `param=input` before upstream execution. String and content
array outputs use the existing message-content conversion.

Tests verify parallel call/result grouping, preserved identifiers and large
integer arguments, plus invalid histories in both JSON and streaming entry points
with zero upstream calls. This conversion follows the native
[tool-use message contract](https://platform.claude.com/docs/en/agents-and-tools/tool-use/handle-tool-calls).

Text processing preserves `call_id` and `tool_call_id` as protocol identifiers.
They are excluded from the remote text projection and retained from the original
input during merging, including when a remote processor returns replacement IDs.
Tool-result text remains transformable. Regression tests verify both identifiers
through local transformation and remote projection merging, preventing
anonymization from breaking the call/result association.

Anthropic Responses history coalesces adjacent messages of the same role.
Within a user turn, tool results precede other content as required by the native
protocol; relative order among results and among other blocks is preserved.
This handles parallel results interleaved with additional user text. A regression
test verifies grouping and block order for text before, between and after results.

### Responses reasoning request options

The optional `reasoning` object accepts `effort`, `summary`, `generate_summary`,
`context` and `mode` string fields. OpenAI-compatible and Ollama Responses adapters
forward supplied fields in both JSON and streaming requests. Upstream/model
support determines valid values; the gateway does not translate them into a
different provider's thinking controls. Anthropic and Demo return
`unsupported_parameter` for a supplied object. Omission preserves prior defaults.

This adds an optional typed request object and its OpenAPI schema. HTTP regression
tests verify all five fields on the wire for both adapters and modes, alongside
unsupported-adapter rejection. Fields follow the
[Responses create contract](https://developers.openai.com/api/reference/cli/resources/responses/methods/create).

Responses usage now retains optional `output_tokens_details.reasoning_tokens`,
using the existing completion-token detail type. Negative values are rejected
before forwarding a native SSE event or returning decoded JSON. This detail is
not added to `output_tokens` or `total_tokens`; those reported counters remain
unchanged. Tests cover positive/zero details, negative rejection and unchanged
totals for JSON and native streaming. The OpenAPI response schema includes the
optional detail object. As with existing token detail types, zero-valued members
may be omitted when serialized while retaining the detail object.

### Responses annotations

Output content retains `annotations` as raw JSON values, preserving provider
citation fields. Native `response.output_text.annotation.added` events populate
the selected output/content part; content and annotation indices are bounded to
0 through 127, while existing output-item bounds still apply. An annotation event
must carry an object or null. Full content/item/response snapshots also preserve
annotations through the response type.

JSON-to-SSE fallback emits annotation events and includes the actual array in the
completed content part; the added part starts with an empty array. Tests verify
JSON preservation, indexed native events, invalid indices and fallback payloads.
The public response schema includes the annotation array. Event fields follow the
[Responses streaming reference](https://developers.openai.com/api/reference/resources/responses/streaming-events).

### Responses context truncation

Responses accepts optional `truncation` (`auto` or `disabled`) and forwards the
explicit value through OpenAI-compatible and Ollama native Responses requests,
including streaming. Omission leaves the upstream default unchanged. The upstream
implements context truncation; the gateway validates the enum and still reserves
tokens against the complete input context before execution. Anthropic conversion
and the demo adapter reject explicit truncation with `400 unsupported_parameter`
and `param=truncation` rather than discarding the requested behavior.

### Responses assistant message phase

Responses output items retain optional `phase` across JSON decoding, native SSE
item and terminal snapshots, and synthesized SSE. Clients replaying assistant
output can preserve `commentary` and `final_answer` without losing their meaning.
The text transformation pipeline excludes phase from text-only projections and
preserves its original value when merging processed content, so anonymization
cannot rewrite this protocol metadata. This adds an optional output field;
messages without phase keep their existing representation.

### Responses output token probabilities

Responses output text retains upstream `logprobs`, including token bytes and
`top_logprobs`. Native SSE deltas append probabilities to the indexed content
part; an explicit done-event list replaces accumulated values. JSON-to-SSE
conversion emits probabilities with text delta/done and completed content.
Clients can request this data with `include: ["message.output_text.logprobs"]`
when supported by the upstream model. These diagnostic values do not alter usage
totals or billing.

Responses also forwards optional integer `top_logprobs` to OpenAI-compatible and
Ollama native Responses endpoints in both JSON and streaming mode, preserving
explicit zero. The gateway validates the 0–20 range before running request
modules or routing; the upstream validates model compatibility. Omission leaves the upstream default unchanged. Anthropic
conversion and demo reject supplied values with `400 unsupported_parameter` and
`param=top_logprobs`, including zero.

Anthropic Responses conversion rejects non-null `phase` on input items with
`400 unsupported_parameter` and `param=input.phase` before issuing an upstream
request. Its native message contract cannot preserve this assistant state.
Omitted or null phase keeps existing conversion behavior. OpenAI-compatible
Responses replay continues to preserve phase unchanged.

The Responses HTTP boundary rejects an invalid `top_logprobs` range or a
`truncation` value other than `auto`/`disabled` with `400 invalid_request` for
both JSON and streaming requests. Omitted and null options remain accepted.
These requests stop before pipeline execution and provider calls.

When a native Responses refusal replaces output text at the same content index,
the collector clears the replaced text's annotations and token probabilities.
This applies to both refusal delta and done events, preventing text diagnostics
from appearing on a refusal in the assembled response.

The router also validates these Responses options before endpoint selection and
affinity lookup. Internal callers and requests modified by ingress modules receive
the same client-error classification. Streaming validation errors are terminal
and cannot trigger a JSON fallback that would discard the error.
