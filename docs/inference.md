# Inference API и совместимость

## Публичные endpoints

Gateway реализует OpenAI-compatible endpoints:

| Endpoint | Назначение |
| --- | --- |
| `GET /v1/models` | Модели, доступные текущему credential |
| `GET /v1/models/{model}` | Метаданные одной доступной модели; скрытая и отсутствующая модель возвращают одинаковый `404` |
| `POST /v1/chat/completions` | Chat, tools, structured output и vision |
| `POST /v1/completions` | Native text completion для строковых и token-ID prompts; JSON и SSE |
| `POST /v1/responses` | Responses, continuity, function tools и MCP passthrough |
| `POST /v1/conversations` | Создание owner-isolated durable conversation с начальными items |
| `GET/POST/DELETE /v1/conversations/{conversation_id}` | Чтение, обновление metadata и удаление conversation |
| `POST/GET /v1/conversations/{conversation_id}/items` | Добавление и cursor pagination conversation items |
| `GET/DELETE /v1/conversations/{conversation_id}/items/{item_id}` | Чтение и удаление отдельного item |
| `POST /v1/responses/input_tokens` | Native-подсчет полного Responses input без generation billing lifecycle |
| `POST /v1/responses/compact` | Native compaction с авторизацией модели и учетом фактического usage |
| `POST /v1/interactions` | Синхронное, incremental SSE или durable background взаимодействие через Responses policy/routing с отдельной billing attribution |
| `POST /model/{model}/converse` | Native Converse request через общий Chat policy/routing и отдельную billing attribution |
| `GET /v1/responses/{id}` | Чтение сохраненного Response владельцем credential |
| `DELETE /v1/responses/{id}` | Удаление сохраненного Response и ownership binding |
| `POST /v1/responses/{id}/cancel` | Отмена сохраненного background Response владельцем credential |
| `GET /v1/responses/{id}/input_items` | Страница исходных input items сохраненного Response |
| `POST /v1/embeddings` | Строки или bounded token-ID inputs |
| `POST /v1/rerank` | Query/documents ranking |
| `POST /v1/audio/transcriptions` | Транскрипция проверенного multipart audio с token или duration billing |
| `POST /v1/audio/translations` | Перевод речи на английский через deployment с явной capability |
| `GET /v1/realtime?model=...` | Ограниченная WebSocket-сессия для text и capability-isolated audio событий |
| `POST /v1/files` | Durable multipart upload с owner quota |
| `GET /v1/files` | Cursor-список файлов текущих credential и user |
| `GET /v1/files/{id}` | Метаданные своего файла |
| `GET /v1/files/{id}/content` | Содержимое своего файла |
| `DELETE /v1/files/{id}` | Удаление своего файла |

Полные payloads, ограничения и ошибки описывает
[OpenAPI](../repos/gateway/api/openapi.yaml). Все endpoints требуют Bearer
credential и применяют тот же model/tool policy, что `/v1/models` и Playground.

Files API доступен только при настроенном PostgreSQL control-plane store. Файлы
изолированы по паре credential/user, ограничены `FILE_MAX_BYTES`, а суммарная
квота `FILE_OWNER_QUOTA_BYTES` проверяется атомарно даже при конкурентных
загрузках. Gateway не сохраняет multipart upload в process-local memory после
завершения запроса.

## Матрица полей запроса

JSON decoder применяет закрытый контракт request types, указанный в OpenAPI
(`additionalProperties: false`). Неизвестные поля верхнего уровня возвращают HTTP 400 `invalid_request` с сообщением
`json: unknown field "имя"` до выполнения pipeline и provider call.
Это намеренное изменение совместимости: раньше неизвестные поля игнорировались.
Например, Responses `background=true` требует `store=true`, durable PostgreSQL
job storage и deployment capability `background_responses`. Распознаваемые параметры перечислены ниже; adapter policy может
отклонить поле до выполнения запроса.

Background Responses можно выполнять внутри owner-isolated conversation. Gateway
сохраняет текущие input items в отдельном pending-состоянии PostgreSQL, не помещая
prompt в payload очереди. Durable turn блокирует параллельные изменения conversation
до terminal settlement. Успешный результат атомарно переносит pending input и output
в историю; failed или cancelled result удаляет pending input и освобождает turn.
Повторная обработка того же execution ID идемпотентна. Admission резервирует до
1024 output items, совпадающих с общей границей Responses output cardinality,
поэтому terminal commit не может зависнуть на item quota.

| Endpoint | Поля контракта верхнего уровня |
| --- | --- |
| `/v1/chat/completions` | `metadata`, `store`, `provider`, `model`, `messages`, `tools`, `tool_choice`, `parallel_tool_calls`, `response_format`, `stream`, `stream_options`, `max_tokens`, `max_completion_tokens`, `temperature`, `top_p`, `stop`, `seed`, `modalities`, `audio`, `reasoning_effort`, `safe_prompt`, `n`, `safety_identifier`, `prompt_cache_key`, `prompt_cache_options`, `prompt_cache_retention`, `prompt_mode`, `prediction`, `service_tier`, `user`, `verbosity`, `web_search_options`, `web_fetch_options`, `logprobs`, `top_logprobs`, `frequency_penalty`, `presence_penalty`, `min_p`, `top_k`, `top_a`, `repetition_penalty`, `logit_bias`; assistant messages may contain signed `reasoning` blocks or bounded `reasoning_content` when the selected adapter supports that history format |
| `/v1/completions` | `provider`, `model`, `prompt`, `metadata`, `best_of`, `echo`, `frequency_penalty`, `logit_bias`, `logprobs`, `max_tokens`, `min_tokens`, `n`, `presence_penalty`, `prompt_cache_key`, `seed`, `stop`, `stream`, `suffix`, `temperature`, `top_p`, `user` |
| `/v1/responses` | `metadata`, `top_logprobs`, `truncation`, `reasoning`, `store`, `include`, `provider`, `model`, `input`, `instructions`, `tools`, `tool_choice`, `parallel_tool_calls`, `text`, `previous_response_id`, `conversation`, `user`, `safety_identifier`, `prompt_cache_key`, `service_tier`, `background`, `stream`, `max_output_tokens`, `max_tokens`, `temperature`, `top_p`, `frequency_penalty`, `presence_penalty`, `max_tool_calls` |
| `/v1/responses/input_tokens` | `provider`, `model`, `input`, `instructions`, `tools`, `tool_choice`, `parallel_tool_calls`, `text`, `previous_response_id`, `reasoning`, `truncation` |
| `/v1/responses/compact` | `provider`, `model`, `input`, `instructions` |
| `/v1/embeddings` | `provider`, `model`, `input`, `metadata`, `input_type`, `encoding_format`, `dimensions`, `output_dtype`, `user` |
| `/v1/rerank` | `provider`, `model`, `query`, `documents`, `top_n`, `rank_fields`, `return_documents`, `max_chunks_per_doc`, `max_tokens_per_doc`, `truncate` |
| `/v1/moderations` | `provider`, `model`, `input`, `metadata` |
| `/v1/images/generations` | `provider`, `model`, `prompt`, `n`, `quality`, `response_format`, `size`, `style`, `user`, `background`, `output_format`, `output_compression` |
| `/v1/images/edits` | Multipart: `image`/`image[]`, `mask`, `provider`, `model`, `prompt`, `n`, `quality`, `response_format`, `size`, `user`, `background`, `output_format`, `output_compression` |
| `/v1/images/variations` | Multipart: `image`, `provider`, `model`, `n`, `response_format`, `size`, `user` |

Матрица описывает входной контракт gateway; возможности конкретной модели и
adapter дополнительно ограничивают допустимые запросы. `provider` управляет
выбором adapter. Свободные JSON-объекты, например `tools[].function.parameters`
и `response_format.json_schema.schema`, сохраняют произвольные свойства:
имена полей пользовательской схемы не считаются параметрами inference.

Provider type `cohere` implements the native v2 Rerank protocol. It sends
Bearer credentials to `/v2/rerank`, preserves fractional billed search units,
and discovers non-deprecated Rerank models through `/v1/models`. The v2 adapter
accepts string documents, `top_n`, and `max_tokens_per_doc`. Object documents,
`rank_fields`, and `max_chunks_per_doc` receive an explicit
`unsupported_parameter` before an upstream call. `return_documents` remains a
gateway response projection and is not forwarded. Responses are limited to
8 MiB, reject trailing JSON, invalid indices, non-finite scores, and negative
token or billed-unit counters. Search units remain observable provider usage;
token-based catalog pricing continues to use the existing bounded input estimate.

The same provider type implements native text embeddings through `/v2/embed`.

Provider type `nvidia-nim` sends native Rerank requests to `/v1/ranking`. The
adapter accepts 1 to 512 non-empty string passages, applies `top_n` locally,
preserves requested documents in the gateway response and forwards optional
`truncate` values `NONE` or `END`. Object documents, rank fields and chunk/token
limits fail before upstream execution. The bounded response must contain one
unique result per passage in descending-logit order and positive provider usage
where `total_tokens` equals `prompt_tokens`; otherwise the request fails before
billing settlement.

NVIDIA NIM Chat publishes `reasoning_effort` as a model-specific capability.
`nvidia/nemotron-3-super-120b-a12b` accepts `none`, `low`, and `high`, while
`nvidia/nemotron-3-ultra-550b-a55b` accepts `none`, `medium`, and `high`. Other
models and values fail before provider HTTP, except the explicit
`deepseek-ai/DeepSeek-V4-Pro-0813` override, which accepts `low`, `high`, and
`max`. The accepted value is forwarded unchanged on the compatible Chat wire.
NIM's top-level `usage.reasoning_tokens` is normalized to public
`completion_tokens_details.reasoning_tokens` in JSON and SSE. Negative,
fractional, null, conflicting or greater-than-completion values fail before the
response enters billing and observability.
NVIDIA NIM exposes the output cap as native `max_tokens`; the adapter maps
public `max_completion_tokens` to that field for JSON and SSE. Both public names
retain the shared mutually-exclusive validation and common TPM/budget reserve.
Both Nemotron 3 model IDs reject output limits outside `1..32768` before HTTP,
for either public limit name.
DeepSeek V4 Pro 0813 responses expose `reasoning_content`, but the provider
contract requires subsequent turns to include only visible assistant content.
Supplying that trace in assistant history therefore fails before provider HTTP.
The two Nemotron 3 model IDs additionally reject `temperature` values above 1
before provider execution; other NIM model policies remain independent.
Their native seed range starts at zero, so negative `seed` values also fail
before provider execution while zero remains valid.

Provider type `together` sends Rerank requests to the native `/v1/rerank`
endpoint with bearer authentication. It accepts text and object documents,
`top_n`, and `return_documents`; unsupported chunking and rank-field controls
fail before upstream execution. Responses are limited to 8 MiB, reject trailing
JSON, and require non-negative provider token usage whose total exactly matches
its input and output components. Missing or inconsistent usage fails the request
before billing settlement can use an estimate.

Together Chat preserves native reasoning output in both JSON and SSE. The
adapter maps the provider's `reasoning` field to public `reasoning_content` and
maps unchanged assistant history back to the native field for the exact models
listed in its capability profile. Conflicting aliases, non-string or oversized
reasoning fail before a response is accepted. The two GPT-OSS models accept
`reasoning_effort` values `low`, `medium`, and `high`; the versioned DeepSeek V4
Pro 0813 ID accepts `high` and `max`. The current
`deepseek-ai/DeepSeek-V4-Pro` accepts `none`, `high`, and `max`.
`reasoning_effort=none` maps to native `reasoning.enabled=false` for that model
and the documented GLM 5/5.1, Kimi K2.5/K2.6, Qwen 3.5/3.6 and Cogito v2.1
hybrid IDs. Other model/value combinations fail before HTTP. The provider
capability response reports these exact model overrides separately from its
provider-wide Chat parameter policy.

Together Chat accepts public `logprobs=true` and maps it to the provider's
integer `logprobs=0` control. Bounded JSON token arrays and numeric SSE
probabilities are normalized into the public selected-token logprobs shape with
exact token bytes. The adapter verifies array lengths, token identifiers,
finite non-positive probabilities and reconstruction of the returned text.
Requested probabilities may not disappear from a content response. Public
`top_logprobs` and non-empty native alternative maps fail because the native
contract does not associate alternatives with individual token positions.
The same native transport forwards `min_p`, `top_k`, `repetition_penalty` and
integer `logit_bias`. Shared request validation restricts probabilities and
candidate counts, requires a positive repetition penalty, and accepts only
nonnegative numeric token IDs with biases from -100 to 100 before provider HTTP.
Together exposes only native `max_tokens`, so the adapter maps public
`max_completion_tokens` to that field for JSON and SSE. The gateway continues
to reject requests containing both public limit names and uses their common
effective value for TPM admission and billing reserve.
Together's native terminal reason `eos` is exposed as public `stop` in JSON and
SSE. The adapter preserves `stop`, `length`, `tool_calls` and the deprecated
`function_call`; pending stream chunks may use `null`. Missing JSON terminal
reasons and unknown values fail before the response or affected stream chunk is
accepted.

The same provider type sends bounded Text-to-Speech requests to native
`/v1/audio/speech`. It preserves model, input, voice and lowercase language or
locale, maps the public PCM format to upstream `raw`, and explicitly sends MP3
when the public default is used. MP3, WAV and PCM responses are limited to
32 MiB and normalized to the requested media type after upstream content-type
validation. Instructions, speed, unsupported formats and SSE fail before HTTP.
Successful requests settle the exact Unicode character count already recorded
by the shared audio-speech lifecycle.

Together Audio Transcription uses native multipart `/v1/audio/transcriptions`
with bearer authentication. It accepts lowercase ISO 639-1 or `auto` language,
JSON or verbose JSON, temperature, and word/segment timestamps. WAV, FLAC,
OGG/Opus, MP3, M4A/MP4 and WebM inputs are admitted only when their container
metadata yields a positive duration of at most four hours. The adapter injects
that exact duration into a bounded, validated upstream response before shared
billing settlement. AAC, prompt bias without a model-specific policy,
diarization that cannot preserve speaker IDs, and batch or streaming controls
fail before HTTP.

Together Audio Translation uses the same duration-gated transport and bounded
response decoder with native multipart `/v1/audio/translations`. It preserves
prompt bias, JSON or verbose JSON response format and temperature. Language,
timestamps, diarization, batch controls and streaming fail before HTTP. The
shared lifecycle reserves and settles the same locally verified duration, so a
successful upstream response without token usage cannot fall back to a byte or
token estimate.
Requests require one of `search_document`, `search_query`, `classification` or
`clustering` in `input_type` and accept at most 96 non-empty texts. Token-ID
input and `user` are rejected before the upstream call. `dimensions` maps to
the native output dimension and is restricted to 256, 512, 1024 or 1536.
Float vectors are validated for count, consistent dimensions and finite values;
`encoding_format: base64` is encoded locally from the validated float response.
Provider-reported billed input tokens take precedence over the bounded local
estimate, including an explicitly reported zero, and flow into post-response
billing.

Native Cohere Chat uses `/v2/chat` with Bearer credentials. It accepts text-only
`system`, `developer`, `user` and `assistant` messages, maps `developer` to the
native `system` role, and supports `max_tokens` or `max_completion_tokens`,
`temperature`, `top_p`, stop sequences, JSON object output and JSON Schema
output. Nonnegative `seed` and native-range frequency and presence penalties are
forwarded explicitly. Provider-reported billed input and output tokens flow
into post-response billing; incomplete counters and malformed response content
fail the request. Deployments with streaming enabled and the `stream` capability
use the native event stream; its lifecycle, content indices, terminal reason and
billed usage are validated before settlement. Other deployments use the gateway's
buffered SSE projection. Cohere tools, media and other unsupported Chat parameters
are rejected or excluded by deployment capabilities. Discovery includes
non-deprecated models advertising the Chat, Embed or Rerank endpoint.

Native Cohere JSON Chat also maps function schemas, `auto`, `required` and
`none` tool selection, consistent strict-tool mode, parallel tool calls,
assistant tool-call history and document-wrapped tool results. Tool names remain
subject to the gateway ACL and tool schemas participate in TPM and billing
reservation. Malformed arguments, unknown history IDs, mixed strict settings,
named tool selection and requests that disable parallel calls fail before the
upstream request. Native tool-call SSE maps sparse upstream indices to stable
output indices, assembles bounded argument fragments, validates complete JSON
objects and settles provider-reported billed usage only after every tool call
has ended successfully.

Provider type `mistral` uses the compatible Chat and Embeddings transports and
implements text completion as native FIM at
`/v1/fim/completions`. The FIM adapter accepts one string prompt, `suffix`,
`max_tokens`, `seed` (sent as `random_seed`), `stop`, `temperature`, `top_p`,
and JSON or SSE execution. It normalizes the provider's chat-shaped FIM result
to the public text-completion response, requires stable stream identity and a
terminal `[DONE]`, validates final usage, and rejects unsupported legacy fields
before an upstream call. Managed model discovery uses `/v1/models` with a
provider-scoped Bearer credential.

`/v1/completions` следует legacy [text completion contract](https://developers.openai.com/api/reference/java/resources/completions/methods/create).
Gateway принимает строку, массив строк, массив token IDs или массив массивов
token IDs и передает параметры только адаптеру с native completion operation.
Пустой или отсутствующий prompt означает начало нового документа. Число prompts,
умноженное на `n`, не может превышать 128. Текстовые элементы независимо проходят
content policy и anonymization; token IDs сохраняются без преобразования и входят
в input TPM по точному количеству. `n` и `best_of` входят в TPM и billing reserve
для каждого prompt; provider usage закрывает фактическое списание. `stream=true`
проксирует native SSE у streaming-capable adapter. Gateway валидирует chunks,
собирает итоговый usage для billing и запрещает retry/fallback после первой записи
клиенту. Placeholder, разделённый между chunks, восстанавливается до отправки.
Если выбранный adapter не поддерживает native stream, gateway выполняет один
ограниченный JSON-вызов и возвращает его как buffered SSE.
Ollama выполняет строковые prompts через свой `/v1/completions` endpoint с
provider-reported usage и native SSE. Для stream gateway запрашивает финальный
usage через provider-specific `stream_options.include_usage`; generic compatible
adapter не получает этот параметр автоматически. Текущий Ollama contract не принимает
prompt arrays; gateway возвращает terminal `unsupported_parameter` до policy
modules, billing reserve и сетевого вызова.

## Capabilities

Поддерживаемые значения: `chat`, `responses`, `embeddings`, `rerank`, `stream`,
`tools`, `structured_output`, `mcp`, `vision`, `web_search`, `ocr`. Gateway выводит требования из
request и исключает несовместимые deployments до provider call.

Legacy endpoint без capabilities сохраняет совместимость с базовыми chat,
responses и embeddings flows, но не является неявным opt-in для `mcp`, `vision`,
`web_search`, `rerank` или `moderation`. Для новых deployments задавайте capabilities явно.

`POST /v1/moderations` принимает одиночный текст, batch строк либо массив `text`/`image_url` частей. Пустые, смешанные и неизвестные вложенные формы отклоняются до provider call. Запрос проходит общие authentication, access, TPM, guardrail, retry и billing стадии. Routing требует явную deployment и model capability `moderation`; при отсутствии model используется `omni-moderation-latest`. Provider response ограничен по размеру и проверяется на число результатов, диапазон scores, одинаковые category keys, допустимые input types и согласованность общего `flagged`. Так как публичный ответ не содержит token usage, billing commit использует консервативную оценку входного текста и помечает usage как estimated. При включенном AV remote image URL отклоняется fail-closed, поскольку gateway не загружает внешний контент от имени scanner; проверенные data image URL передаются scanner как bounded attachment.

`POST /v1/ocr` принимает HTTPS/data URL либо `document: {"type":"file","file_id":"file_..."}`. Ссылка на файл разрешается только внутри пары authenticated credential и user, затем содержимое проверяется по MIME, сигнатуре и лимиту 16 MiB и преобразуется во внутренний data URL до DLP/AV и provider pipeline. Отсутствующий или чужой файл возвращает одинаковый `404`; неподдерживаемый либо поврежденный файл не передается provider adapter.

Native Gemini OCR принимает inline PDF, PNG, JPEG и WebP, включая уже
разрешенные owner-scoped file references. Adapter запрашивает schema-constrained
массив страниц с Markdown, проверяет уникальность и точное совпадение выбранных
page indices и фиксирует фактически возвращенное число страниц. Remote URLs,
image extraction, confidence/blocks, header/footer и annotation controls
отклоняются до upstream call. Native Mistral сохраняет расширенный OCR contract.

`/v1/skills` и `/v1/skills/{id}/versions` публикуют capability-gated lifecycle для native Anthropic deployment. Multipart packages ограничены 32 MiB, ответы — 8 MiB, а произвольные provider paths не проксируются. Custom skill после создания атомарно связывается с хешированным credential+user owner key и исходным deployment. List скрывает custom skills других владельцев; чтение shared read-only sources разрешено, а workspace-specific plugin resources скрываются. При сбое ownership claim gateway компенсирует создание upstream delete-запросом и возвращает fail-closed ошибку.

Native Mistral moderation принимает только строку или массив строк и отклоняет
структурированные text/image parts до provider modules, billing и upstream.
Bounded `metadata` передается native Mistral Moderations; остальные adapters
отклоняют его до выполнения.
Provider categories и scores проходят общий validator; отсутствующий native
`category_applied_input_types` нормализуется в `text` только для этого
предварительно проверенного text-only запроса.

### Image generation

`POST /v1/images/generations` выполняется только через deployment и model с
capability `image_generation`. Gateway проверяет prompt и параметры до policy
pipeline, учитывает prompt при TPM, резервирует output на каждый запрошенный
image и запускает обычные admission, retry, failure и billing phases. Каталог
может задать `image_cost_per_unit`: reserve использует запрошенное `n` (по
умолчанию один), а commit — число результатов, прошедших проверку ответа.
Зафиксированная при reserve цена применяется к retry и commit без переоценки.
Совместимые и Azure deployments используют JSON transport семейства Images.
Native Gemini deployment преобразует запрос в GenerateContent с image-only
response modality. Он поддерживает один inline `b64_json` результат, точные
`aspect_ratio` из native API и `resolution` 512/1K/2K/4K. Параметры без точного
native эквивалента, URL output и `n` больше единицы отклоняются до upstream.
Gemini `usageMetadata.totalTokenCount` используется для полного settlement,
включая reasoning и другие учтенные upstream output tokens.

Native Together deployment вызывает `/v1/images/generations`, ограничивает `n`
диапазоном 1–4 и явно преобразует `size` в `width`/`height`, а `b64_json` — в
native `base64`. Поддерживаются URL/base64 response, JPEG/PNG output и seed;
остальные публичные controls отклоняются до HTTP. Ответ обязан содержать точные
ordered indices, ожидаемые model/object и ровно запрошенное число bounded
результатов. Так как provider contract не возвращает token usage, token fields
остаются помечены как estimated, а стоимость рассчитывается по точному числу
проверенных изображений.

`POST /v1/images/edits` использует тот же общий lifecycle для совместимых,
Azure и native Gemini deployments. Gemini transport передает prompt и от одного
до восьми проверенных PNG/JPEG/WebP изображений как inline GenerateContent parts
и принимает ровно один base64 image result. Mask editing, URL output, несколько
результатов и параметры без точного native эквивалента отклоняются до сети.

`POST /v1/images/variations` также поддерживает native Gemini через image-to-image
GenerateContent: один проверенный PNG/JPEG/WebP input преобразуется в один inline
base64 result. Gateway задает нейтральную variation instruction; URL output,
несколько результатов, exact size и user metadata отклоняются до upstream.

Native Gemini deployments с capability `audio_transcription` передают проверенный
WAV/MP3/MPEG/AIFF/AAC/OGG/Opus/FLAC/M4A/WebM input в GenerateContent
transcription config. Language hints, custom vocabulary, prompt guidance,
temperature и проверенный
`mode=VERBATIM|SMART` отображаются явно;
provider token usage проходит общий exact settlement. `timestamp_granularities=[word]`
передается как word timestamps и возвращается в `verbose_json`; `diarized_json`
включает speaker diarization и возвращает bounded segments. Некорректные offsets,
отсутствующие annotations и несовместимые сочетания с SMART или vocabulary
отклоняются. Chunking и known-speaker режимы отклоняются до upstream.

Ответ ограничен 64 MiB, содержит ровно запрошенное число результатов и для
каждого результата допускает ровно один источник: HTTP(S) URL без credentials
либо корректный base64 размером до 20 MiB после декодирования. Token usage
обязателен и должен быть неотрицательным с точной суммой для текущих adapter
contracts. `image_cost_per_unit` начисляется независимо по фактически
возвращённым результатам. Streaming остаётся отдельным контрактом и требует
terminal usage event.

`POST /v1/images/edits` принимает `multipart/form-data` только для deployment и
model с capability `image_edit`. Допускается до 8 файлов суммарно: одно или
несколько входных изображений и один optional PNG mask; не более 8 MiB на файл
и 16 MiB суммарно. Gateway сверяет заявленный MIME type с
сигнатурой PNG, JPEG, GIF или WebP, передает prompt в DLP, а изображения и mask —
в AV. Неизвестные и повторные scalar fields отклоняются. TPM reserve и billing
settlement используют тот же bounded output contract, что и generation; успешный
provider response обязан содержать точный token usage.

`POST /v1/images/variations` принимает одно проверенное изображение размером до
8 MiB для deployment и model с capability `image_variation`. Изображение
передается в AV без текстовой проекции. TPM и billing reserve учитывают bounded
image input и число запрошенных результатов; успешный ответ проходит общий
validator количества, URL/base64 и точного token usage.

### Audio transcription

`POST /v1/audio/transcriptions` принимает один файл AAC, AIFF, FLAC, MP3, MP4,
MPEG, MPGA, M4A, OGG, Opus, WAV или WebM размером до 20 MiB для deployment и
model с capability
`audio_transcription`. Gateway сверяет расширение, MIME type и сигнатуру файла,
передает optional prompt и `keywords[]` в DLP, а аудиоданные — в AV. Подсказки
`languages[]` и `keywords[]` ограничены по количеству и размеру. Стратегия
`chunking_strategy` принимает `auto` или строгий JSON-объект `server_vad` с
bounded `prefix_padding_ms`, `silence_duration_ms` и `threshold`. Все эти поля
учитываются в TPM и billing reserve. `known_speaker_names[]` и
`known_speaker_references[]` принимаются только парами до четырех элементов.
Reference должен быть inline base64 data URL поддерживаемого аудиоформата с
корректной сигнатурой; лимит составляет 512 KiB на образец и 2 MiB суммарно.
Все reference-аудио передается в AV, имена — в DLP, а их текст и байты входят в
TPM и billing reserve. Требование провайдера к длительности образца проверяется
upstream, поскольку оно не выводится надежно из bounded bytes для всех сжатых
форматов. Неизвестные и повторные scalar fields, а также превышающие лимиты
bounded array fields отклоняются.

Поддерживаются JSON-форматы `json`, `verbose_json` и `diarized_json`. Успешный
ответ ограничен 8 MiB, transcript — 1 MiB текста, words и segments — 100 000
элементов суммарно. Token-priced provider обязан вернуть точный token usage с
согласованной суммой. Duration-priced provider возвращает отдельный usage type с
нулевыми token counters и положительной billable audio duration.

При `stream=true` compatible и Azure deployment с включенным upstream streaming
возвращает SSE-события `transcript.text.delta`, опциональные
`transcript.text.segment` для `diarized_json` и обязательное терминальное
`transcript.text.done`. Gateway ограничивает общий ответ 8 MiB, проверяет каждое
событие, требует точного usage в `done` и сверяет финальный text с накопленными
delta. Retry и fallback разрешены только до первой записи клиенту. После первой
записи ошибка завершается SSE error и не запускает второй deployment, исключая
смешанный transcript. Если выбранная policy требует проверки полного output или
upstream streaming выключен, gateway выполняет обычную транскрипцию через весь
policy и billing lifecycle и возвращает один проверенный `transcript.text.done`.
Capability profile публикует `stream` только для deployment, где transport
действительно включен. `stream=true` для translation отклоняется до выполнения
pipeline и provider call.

Native Mistral transcription передает `language`, `temperature`,
`timestamp_granularities` и `keywords[]` как `context_bias`; `diarized_json`
включает diarization. Параметры без точного native соответствия отклоняются до
сетевого вызова. Billing использует `prompt_audio_seconds` провайдера при
commit. Для WAV, FLAC, OGG, MP3 и завершенного audio-only WebM reserve
рассчитывается из RIFF, STREAMINFO, Ogg granule metadata, полного scan Layer III
frames или WebM Segment Info с округлением вверх до миллисекунды. Для остальных
WebM резервируется документированный предел 60 минут, чтобы контейнер не мог
занизить duration budget. Native Mistral streaming отклоняется явно до provider
call.

Native Groq transcription передает `language`, `prompt`, `temperature` и
`timestamp_granularities`, а upstream всегда запрашивает `verbose_json`, чтобы
сохранить доступные timing metadata. Prompt ограничен консервативной оценкой в
224 tokens. Billing резервирует и фиксирует минимум 10 секунд. Adapter принимает
WAV, FLAC, однопоточные OGG Opus/Vorbis, MP3 Layer III, MP4/M4A с полной media
duration и завершенный audio-only WebM: RIFF, STREAMINFO, Ogg granule metadata,
полный frame scan, `mdhd` первого `soun` track или WebM Segment Info позволяют
определить длительность до provider call. Multiplexed WebM, unknown-size nested
elements и fragmented MP4 без полной track duration отклоняются до добавления
полного track timeline parser, чтобы контейнер не мог обойти duration budget.

`POST /v1/audio/translations` использует тот же multipart request, проверки
файла, DLP/AV policy, model authorization, TPM reserve, retry и billing lifecycle.
Маршрутизация требует отдельную capability `audio_translation`, поэтому
transcription-only deployment не выбирается для перевода. Native Groq adapter
вызывает `/audio/translations`, принимает `prompt`, `temperature`,
`response_format` и только `language=en`; timestamp granularities отклоняются до
upstream call. Для учета применяется проверенная длительность контейнера и
минимум десять оплачиваемых секунд.

Native Gemini adapter отправляет WAV, MP3/MPEG, AIFF, AAC, OGG/Opus, FLAC, M4A
или WebM как bounded
inline input в GenerateContent и явно просит английский перевод. Поддерживаются
`prompt`, `temperature` и JSON response envelope; параметры транскрипции,
включая исходный язык кроме `en`, списки языков, vocabulary, timestamps,
chunking и speaker references, отклоняются до upstream call. Учет фиксирует
точные token counters из ответа Gemini.

Compatible и Azure adapters передают `prompt`, `temperature` и JSON response
format. Они отклоняют transcription-only параметры до сетевого вызова. Поскольку
translation response может не содержать token usage, gateway принимает только
WAV, FLAC, OGG, MP3, завершенный MP4/M4A или audio-only WebM с проверяемой
длительностью и фиксирует эту длительность при отсутствии точных upstream
counters. Неполные и неоднозначные контейнеры отклоняются до budget reserve.

`POST /v1/audio/speech` с `stream_format=sse` использует compatible или Azure
deployment с включенным upstream streaming. Gateway пропускает только
`speech.audio.delta` с непустым корректным base64 audio и обязательное
`speech.audio.done` с согласованным точным token usage. Суммарный decoded audio
ограничен 32 MiB, а wire stream — 64 MiB. Retry и fallback допустимы только до
первого события; после него ошибка завершается SSE error без смешивания audio от
другого deployment. Complete-output policy и adapters без подтвержденного SSE
возвращают `streaming_unsupported`. Capability profile содержит отдельный
`sse_supported`, вычисленный из transport configuration и runtime validator.
Character billing остается точным по преобразованному input, а terminal usage
фиксируется как точный provider token usage.

Native Gemini deployment с capability `audio_speech` вызывает Interactions API,
запрашивает inline audio и проверяет completed model-output до декодирования.
Поддерживаются voice, natural-language `instructions` и форматы MP3, Opus, WAV
и raw PCM. `speed`, AAC и FLAC отклоняются до upstream call. Base64 envelope и
декодированный audio ограничены независимо; billing фиксирует точное число
Unicode-символов исходного текста.

`POST /guardrails/apply_guardrail` выполняет enabled DLP/AV policy без model inference. Обычный virtual key может вызвать только policy, которая совпала с его durable attachment; admin role может проверять любую enabled policy. Если указан `model`, gateway также применяет model, access-group и tag grants. Каждый вызов учитывается в RPM/TPM и требует доступного durable audit до scanner call; итоговый audit содержит только policy, outcome и статусы checks. Текст ограничен 64 KiB, не возвращается клиенту, не записывается в audit или guardrail monitor и не открывает generation billing lifecycle. Отказ policy registry, audit или scanner приводит к fail-closed `503`.

Vision принимает только inline `data:image/{jpeg,png,gif,webp};base64,...`.
Remote URLs запрещены. AV должен быть включён; media type проверяется по
signature. Лимиты: 8 изображений, 8 MiB каждое, 16 MiB decoded total и 24 MiB
на JSON body.

## Provider adapters

| Type | Особенности |
| --- | --- |
| `openai`, `openai-compatible`, `openrouter` | OpenAI wire format, including bounded compatible `reasoning_content` passthrough |
| `azure-openai` | Native Azure OpenAI HTTP and Realtime WebSocket URLs, API version, API key, static Entra token, public/US Government/China cloud identity selection, AKS workload federation or refreshable ambient managed identity |
| `anthropic` | Преобразование chat/tools/vision в native Messages API |
| `ollama` | Native chat/stream/embeddings и provider completions JSON/SSE для строкового prompt; native `top_k`, `min_p`, log probabilities и reasoning history/output |
| `gemini` | Native GenerateContent chat/stream, tools, inline vision, structured output, text embeddings, audio transcription/translation and schema-constrained OCR; Interactions text-to-speech; API key or GCP workload identity |
| `mistral` | Native Chat JSON/SSE and embeddings wire contract; FIM completions; Bearer API key |
| `voyage` | Native text embeddings and rerank; Bearer API key |
| `bedrock` | Native Converse chat/tools and JSON Schema output; bearer mode for compatible private endpoints or AWS SigV4 with explicit credentials, environment keys, bounded shared credentials profiles, regional web-identity STS, ECS/EKS container roles and EC2 IMDSv2 instance roles |
| `groq` | Chat/stream, tools, structured output, vision, user attribution and service tiers |
| `deepseek` | Chat/stream and Responses with provider-specific validation and reasoning history passthrough |
| `cerebras` | Chat/stream with bearer authentication, model discovery, function tools, JSON Schema output, reasoning/logprobs/service-tier validation and normalized reasoning content; unsupported fields fail before upstream execution |
| `nvidia-nim` | Chat/stream, native Messages/stream and count-tokens, legacy Completions, Responses create/stream/retrieve/cancel, Embeddings and native text Rerank with optional bearer authentication and model discovery; Rerank supports 512 passages, `NONE`/`END` truncation and exact provider token settlement; stored response lifecycle uses the original deployment ownership binding, Chat and Messages use isolated cache scopes, and model-dependent multimodal input is enabled per deployment |
| `together` | Chat/stream with bounded native reasoning aliases, exact model-specific reasoning-effort policy, normalized selected-token log probabilities and validated min-p, top-k, repetition-penalty and token-bias controls, legacy Completions, Embeddings, native Rerank, Image Generation, duration-accounted Audio Transcription/Translation, bounded Text-to-Speech and model discovery with bearer authentication; Rerank requires exact provider usage, image output uses unit accounting, audio uses exact duration or character settlement, tools, structured output and vision are capability-gated, and unsupported Responses, top-logprob alternatives or silently ignored parameters fail before upstream execution |
| `xai` | Chat/stream, Responses and Embeddings with bearer authentication, merged text/embedding model discovery, structured output, vision, web search, response compaction and owned retrieve/input-items/delete lifecycle; priority tier, bounded reasoning/logprobs validation, float/base64 vectors and exact embedding token usage |
| `demo` | Локальный deterministic fallback для разработки |

OpenAI-compatible adapter один раз повторяет запрос с
`max_completion_tokens`, только когда upstream явно отверг legacy
`max_tokens`. Unsupported non-default `temperature` не переписывается
молча: клиент должен отправить допустимое для модели значение.
Mistral Chat преобразует публичные `seed` и `max_completion_tokens` в native
`random_seed` и `max_tokens`, а embeddings `dimensions` в `output_dimension`; JSON, SSE и embeddings responses проходят
общую bounded validation и reported usage accounting.
Native Chat передает `metadata`, `n`, `prediction`, `prompt_cache_key`,
`reasoning_effort` до `xhigh`, а также frequency и presence penalties.
Параметры compatible API, отсутствующие в native Chat contract, отклоняются до
modules, billing и provider call. Публичный `stream_options.include_usage`
управляет только gateway-ответом и не отправляется provider-у.
Ошибки transport и parameter validation сохраняют provider identity `mistral`.
Mistral embeddings принимает только строку или массив строк; token-ID input,
`input_type` и `user` отклоняются до provider modules, billing и upstream.
Bounded `metadata` передается native Mistral Embeddings; остальные adapters
отклоняют этот provider-specific параметр до выполнения.
`output_dtype` поддерживает native `float`, `int8`, `uint8`, `binary` и
`ubinary`. Gateway проверяет числовые диапазоны и целочисленность quantized
ответов, а для packed binary сравнивает `dimensions` с числом битов. Та же
проверка учитывает ширину dtype для `encoding_format=base64`.

Voyage adapter принимает до 1000 текстовых embedding inputs, `input_type`,
`dimensions`, поддерживаемые output dtypes и `base64`. Token-ID input,
provider metadata и `user` отклоняются до upstream call. Rerank принимает до
1000 непустых строковых документов и преобразует публичный `top_n` в native
`top_k`. Оба вызова отправляются с `truncation=false`, чтобы upstream не менял
вход молча, а provider-reported `total_tokens` обязателен и используется для
billing. Возвращаемые vectors, indices, scores и usage проходят bounded
validation общей gateway-цепочки.
Mistral не объявляет унаследованный compatible rerank transport: даже ошибочно
настроенная capability исключается router до modules, billing и network call.
Chat `web_search_options` также отклоняется до выполнения: provider-managed web
search не представлен native Chat Completions transport этого adapter.
Mistral Chat принимает `safe_prompt` и передает явно заданные `true` и `false`
в native request. Другие adapters отклоняют этот provider-specific параметр.
`prompt_mode=reasoning` передается только в native Mistral Chat; прочие значения
и другие adapters получают явную ошибку до выполнения.
Для Mistral и Anthropic доступен assistant prefix: `prefix=true` разрешен только
у последнего assistant message с непустым текстом. Маршрутизация требует
capability `assistant_prefill`; остальные adapters отклоняют `messages.prefix`.
Mistral FIM дополнительно передает `metadata`, `min_tokens` и `prompt_cache_key`;
compatible и Ollama adapters отклоняют эти поля до выполнения.

## Routing и модели

Public model может ссылаться на упорядоченную Model Group. `priority` задаёт
fallback tiers, `weight` распределяет трафик внутри tier, adaptive strategy
учитывает EWMA latency/failures. Same-deployment retry разрешён только для
transient failure classes и учитывает bounded backoff/`Retry-After`.

Каждый deployment может задать `rate_limit_rpm` и `rate_limit_tpm`. Нулевое
значение отключает соответствующий предел. Допуск атомарно резервирует один
маршрутизированный запрос и полную оценку input/output токенов на фиксированную
минуту до provider call; при исчерпании router пробует следующий разрешённый
deployment. При Redis счетчики общие для всех gateway replicas.
Managed provider может задать такие же общие пределы для суммы вызовов всех
связанных deployments. Provider и deployment scopes резервируются атомарно;
отклоненный одним scope запрос не расходует другой.

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

Exact cache также учитывает native message blocks, native input reserve,
Bedrock service tier, latency selection и additional response-field paths. Opaque native blocks отключают
semantic cache; native controls входят в его settings fingerprint.

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
| Anthropic Responses | `previous_response_id`, `safety_identifier` |
| Ollama Responses | `safety_identifier` |
| Ollama native chat | `tool_choice`, `parallel_tool_calls` |
| Ollama embeddings | token-ID input; `user`; `encoding_format`, отличный от `float` |
| Gemini embeddings | token-ID input; `user`; `encoding_format`, отличный от `float` |
| Native adapters without a tier contract | `service_tier`; Anthropic Chat accepts only `auto` and `standard_only` |
| Anthropic, Ollama, Gemini и demo Chat | `prompt_cache_key` |
| Anthropic, Ollama и demo Responses | `prompt_cache_key` |
| Anthropic, Ollama, Gemini и demo Chat | `verbosity` |
| Anthropic, Ollama и demo Responses | `text.verbosity` |

Остальные верхнеуровневые поля действующего OpenAI-compatible контракта
передаются соответствующим upstream wire request. Это не подтверждает поддержку
параметра каждой моделью: upstream может вернуть собственную ошибку.
Native adapter ограничения выше являются изменением совместимости для клиентов,
которые раньше отправляли эти поля и получали ответ с молча потерянной настройкой.

OpenAI-compatible embeddings поддерживает `encoding_format=float` и
`encoding_format=base64`. Base64-ответ сохраняется без преобразования, но gateway
проверяет строгую кодировку, непустой little-endian float32 buffer, конечность
значений, предел 65 536 dimensions и соответствие запрошенному `dimensions`.
Ollama, Gemini и demo отклоняют `base64` до обращения к upstream.

OpenAI-compatible SSE учитывает `usage` из финального события с пустым `choices`:
reported prompt/completion/total tokens и prompt-cache details доходят до
post-response billing. Это событие не создаёт дополнительный choice и сохраняется
при проксировании клиенту. Для Chat SSE gateway всегда отправляет upstream
`stream_options.include_usage=true`. Если upstream всё равно не присылает usage,
остаётся существующий estimated fallback; точный учет не выводится из отсутствующих
данных. Отрицательные token counts, переполнение и `total_tokens` меньше суммы
prompt/completion отклоняются до cache, billing и доставки SSE-события клиенту.

Клиент Chat SSE получает финальный usage chunk только при
`stream_options.include_usage=true`. Этот параметр разрешён только вместе со
`stream=true`. Gateway продолжает запрашивать и собирать upstream usage для
billing независимо от выбора клиента; если native stream не создаёт отдельный
usage event, gateway формирует его из проверенного итогового результата.

`stream_options.include_obfuscation` по умолчанию включён. Gateway сохраняет
upstream obfuscation или добавляет случайное padding-поле к Chat delta events и
выравнивает их размер по 256-byte buckets. Это одинаково работает для compatible,
native и synthetic streams. Явное `false` передаётся compatible upstream и
удаляет obfuscation из клиентского потока; поле не влияет на token usage и billing.

Chat `web_search_options` принимает `search_context_size=low|medium|high` и
optional approximate location. Compatible adapter передаёт оба поля без
преобразования. Native Anthropic adapter преобразует запрос в server-side web
search tool с лимитом пять поисков и передаёт approximate location; непустой
`search_context_size` отклоняется как непредставимый параметр. Остальные native
adapters возвращают `unsupported_parameter`. Маршрутизация требует явно заявленную
deployment/model capability `web_search`. Exact и semantic response cache
отключены, поскольку результат зависит от внешнего состояния веба.

Responses принимает current и versioned `web_search`/`web_search_preview`
контракты как одну capability. До provider call billing резервирует одинаковый
консервативный лимит поисков для каждого варианта; `max_tool_calls` сужает этот
лимит, а commit заменяет оценку фактическим числом `web_search_call` outputs.

Chat assistant messages preserve nullable `refusal` in compatible request
history, JSON responses, live SSE accumulation and synthetic SSE. Refusal text
is included in context token estimates and follows the same anonymization and
deanonymization boundary as message content. Native adapters reject historical
refusal fields explicitly instead of silently converting them to ordinary text.

Non-streaming compatible Chat responses preserve typed `url_citation`
annotations. Gateway validates at most 128 citations, nonnegative ordered
offsets, bounded titles and HTTP(S) URLs before cache or client delivery.
Annotations are response-only: request history containing them is rejected.
Native Anthropic Chat responses convert web-search citations to the same contract
and expose them in JSON and streaming deltas. Citation offsets are Unicode code
point indexes in the assembled assistant text. Malformed citation types, URLs,
titles, offsets and cited text fail before delivery and billing commit.


## Chat generation controls

Chat `modalities` accepts unique `text` and `audio` values. Audio output requires
a bounded `audio` object with a supported format and string or custom-ID voice.
Compatible JSON and SSE responses preserve a validated audio ID, base64 data,
expiry and transcript; SSE fragments are bounded and assembled for usage and
post-processing. Assistant history may reference prior audio by ID only. Audio
requests require explicit deployment/model capability `audio` and bypass exact
and semantic response caches. Native adapters reject audio controls and history
before execution. Reported `completion_tokens_details.audio_tokens` remains part
of the validated usage used by billing.
History containing an audio ID must set `provider` to the exact deployment name;
weighted selection and model fallback are disabled because the ID is owned by
the upstream deployment that created it.
If request anonymization replaces any prompt value, audio generation stops before
the upstream call because binary speech cannot be deanonymized consistently.

## Realtime audio

`GET /v1/realtime?model=...` открывает WebSocket только после проверки Bearer
credential, model policy и выбора deployment с capability `realtime`. Text-only
сессии требуют только `realtime`. События `input_audio_buffer.*` и настройка
input audio дополнительно требуют `audio_input`; audio output и transcript
события требуют `audio`. Это не позволяет text-only deployment молча принимать
или возвращать бинарные данные.

Поддерживаются legacy-форматы `pcm16`, `g711_ulaw`, `g711_alaw` и их текущие
эквиваленты `audio/pcm` с частотой 24000 Hz, `audio/pcmu`, `audio/pcma`.
`input_audio_buffer.append` принимает непустой strict base64 до 15 MiB decoded
данных на событие. Gateway хранит только счетчик накопленного объёма с пределом
1 GiB на сессию, передаёт каждый chunk настроенному AV scanner и не отправляет
отклонённый chunk провайдеру. `clear` обнуляет неподтверждённый буфер, а manual
или server-VAD commit добавляет консервативную оценку audio tokens в conversation
context до следующего TPM и budget reserve. Удаление связанного conversation item
снимает эти tokens из последующих резервов.

Provider audio deltas проверяются как strict base64 до передачи клиенту. Точный
`response.done.usage` заменяет резерв при billing commit и сохраняет cached,
text и audio token details. WebSocket event ограничен 20 MiB плюс 64 KiB JSON
overhead, pending responses — 16, conversation items — 1024, а сессия — 30 минут.
Azure deployment с пустым `api_version` подключается к native GA
`/openai/v1/realtime?model=...`; versioned deployment использует preview
`/openai/realtime?api-version=...&deployment=...`. Handshake передаёт только
настроенный `api-key` либо Entra bearer token, включая ambient managed identity.
Отсутствующий API key закрывает запрос до WebSocket dial.

Realtime input transcription является отдельным ASR execution с собственным
usage и ценой модели. Пока для него не настроен независимый pricing, admission
и billing lifecycle, gateway отклоняет `session.type=transcription`, legacy
`input_audio_transcription`, текущий `audio.input.transcription` и неожиданные
provider transcription events. Клиент получает `unsupported_feature`, а
конфигурация не достигает provider WebSocket.

`n` принимает от 1 до 128 choices для OpenAI-compatible adapter. TPM и budget
reserve умножают per-choice output limit (включая default reserve) на `n` с
насыщением при переполнении. Native adapters отклоняют `n` до provider modules.

`safety_identifier` передается OpenAI-compatible adapter и ограничен 64 Unicode
characters. Он входит в exact и semantic cache keys, но не заменяет
аутентифицированные `user_id`/credential identity в billing. Native adapters
возвращают явный `unsupported_parameter`.

Legacy Chat `user` передаётся OpenAI-compatible adapter в JSON и streaming
requests. Поле входит в exact и semantic cache scopes, чтобы локальный cache не
объединял разные provider hints. Оно никогда не заменяет аутентифицированные user
или credential identity при authorization, rate limiting и billing. Native
adapters возвращают явный `unsupported_parameter`.

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
`/v1beta` or `/v1` base). `auth_type=api_key` sends a provider-scoped write-only
credential only in `x-goog-api-key`. `auth_type=gcp_adc` obtains a short-lived
bearer token from the fixed GCE metadata endpoint; GKE Workload Identity
Federation exposes the same endpoint. Metadata and provider redirects are not
followed. Managed deployments
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
Native Responses use their own adapter contract. Advanced inbound GenerateContent
options remain separate gaps. Native Interactions supports model and agent execution,
streaming, durable background processing, stored lifecycle operations and owner-bound
reuse of provider-created agent environments.
Unsupported generation controls, parallel tool control and strict function
schemas fail explicitly; seed/output limits must fit the native integer range.

Protocol references: [GenerateContent](https://ai.google.dev/api/generate-content),
[URL Context](https://ai.google.dev/gemini-api/docs/generate-content/url-context)
[Google Maps grounding](https://ai.google.dev/gemini-api/docs/generate-content/maps-grounding)
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
control, temperature, top-p, nonnegative max_tokens, stop_sequences, stream, `service_tier=auto|standard_only`, opaque
`metadata.user_id`, output effort, native thinking configuration, signed/redacted thinking history and JSON Schema formatting. Provider-specific
capability checks still apply after conversion. Responses contain native text or
tool-use blocks, signed `thinking` blocks, opaque `redacted_thinking` blocks and
native usage fields. Thinking is never projected into ordinary response text.
Cached prompt tokens are separated from
uncached input tokens without changing the internal accounting totals.

SSE emits message_start, content_block_start/delta/stop, message_delta and
message_stop. Function argument fragments use input_json_delta; reasoning uses
thinking_delta and signature_delta while redacted payloads remain opaque. Errors after
stream start emit an error event without successful completion. Native streaming
and the ordinary-response fallback both use the same output conversion. The
conversion buffers at most 32 MiB per JSON response or unfinished SSE frame and
at most 128 concurrent tool identities. Accumulated function arguments have a
separate 32 MiB total limit and must form JSON objects before completion. A
stream without a finish reason fails.

Compatibility is partial. Unsupported top-level fields and block fields fail
with a native invalid_request_error. Thinking blocks are accepted only in
assistant history, must precede text/tool blocks, retain their provider signature,
and have a 1 MiB aggregate payload limit. Adapters without an explicit reasoning
block contract reject them before upstream execution. User messages accept up
to five base64 PDF or UTF-8 plain-text `document` blocks. PDFs have signature
validation, a 16 MiB decoded aggregate limit, attachment policy projection and
`file_input` capability routing. Plain text is limited to 262,144 Unicode
characters per document and 1,048,576 characters per request and requires
`document_text`. Both forms enter DLP and anonymization, conservative TPM and
billing admission, response-cache exclusion, native generation, token counting
and durable Messages batches.
A document may use `source: {"type":"file","file_id":"file_..."}` for an
owner-scoped Files object with `purpose=user_data`. The gateway resolves stored
PDF or UTF-8 plain text after authentication and before DLP, anonymization, TPM
and billing reserve. Missing, expired, foreign, malformed and unsupported files
share one unavailable error. Count-tokens runs policy without generation billing.
Batch creation stores an immutable resolved snapshot and enforces its 4 MiB
per-line limit, so later file deletion cannot change an accepted job.
A PDF document may instead use
`source: {"type":"url","url":"https://documents.example/report.pdf"}`. The
gateway fetches it after authentication without forwarding client credentials,
accepts only `application/pdf`, applies the same 16 MiB bound and PDF signature
validation, and resolves it before DLP, TPM and billing. Fetches require a public
HTTPS destination, TLS 1.2 or newer, do not use environment proxies or follow
redirects, reject credentials and fragments, re-check resolved IP addresses and
time out after 15 seconds. Token counting uses the same resolved content without
generation billing. Batch creation stores the downloaded bytes, so later URL
changes or failures cannot alter an accepted job.
A document may request native citations with `citations: {"enabled": true}`.
The setting is preserved by generation, token counting and durable Messages
batches, requires the explicit `document_citations` deployment capability,
participates in exact-cache identity and bypasses semantic cache reuse. Omission
leaves citations disabled. Citations must be enabled for every document in a
request or omitted from every document; `enabled: false` and mixed requests are
rejected before provider execution.
Optional document `title` and `context` are limited to 512 and 8192 Unicode
characters respectively. They enter DLP scanning and anonymization before
provider execution, contribute to TPM and budget admission, and are preserved
by generation, token counting and durable batches. Requests require the
`document_metadata` deployment capability, include metadata in exact-cache
identity and bypass semantic cache reuse.
Native image blocks accept bounded base64 input or
`source: {"type":"url","url":"https://images.example/chart.png"}`. URL
images use the same post-authentication public-address transport as URL PDFs,
accept JPEG, PNG, GIF and WebP, and are converted to inline data before AV/DLP,
TPM, cache isolation, routing and billing. Per-image and aggregate decoded limits
are 8 MiB and 16 MiB, with at most eight images and mandatory signature checks.
Token counting uses resolved bytes without generation billing; durable batch
creation snapshots the image so later URL changes do not affect execution.
An image may also use `source: {"type":"file","file_id":"file_..."}` for an
owner-scoped `user_data` Files object containing JPEG, PNG, GIF or WebP. The
gateway resolves it only inside the authenticated credential and user boundary,
then applies the same signature, per-image and aggregate limits before policy,
accounting and provider execution. Missing, foreign, expired, malformed and
unsupported files share the unavailable response. Durable batches snapshot the
resolved image, so later deletion cannot alter queued work.
Text after tool_use is not supported. Ordinary client
`tool_result` blocks may set `is_error=true`; the flag is preserved by native
generation, token counting and durable batches. Such requests require the
`tool_result_error` deployment capability, include the flag in exact-cache
identity and bypass semantic cache reuse.
All tool-use history requires matching results. Opaque provider tool metadata
that cannot be represented in Messages produces an explicit conversion error.
`metadata.user_id` is limited to 512 Unicode characters and remains request
metadata; it does not replace gateway authorization or billing identities.
Supported effort values are `low`, `medium`, `high`, `xhigh`, and `max`.
Native `thinking` accepts adaptive, disabled, and legacy enabled modes. Adaptive
thinking may select `display=summarized|omitted`; enabled thinking additionally
requires `budget_tokens >= 1024` and a budget smaller than `max_tokens`.
Temperature must be omitted or exactly 1 for adaptive and enabled modes. Routing
requires the explicit `thinking` capability on a native Anthropic deployment.
The same configuration is forwarded to native token counting and durable batch
execution. Exact and semantic response caches are bypassed. The existing
`max_tokens` reserve remains the total output bound, including thinking, and
provider-reported usage settles the complete output once.
The deprecated `top_k` sampling control is also forwarded to the native
Messages adapter for values from 0 through 1,000,000 and is exposed by its
managed parameter profile. Support remains model-specific: newer models may
reject any supplied value, and that provider error is returned without fallback
to an adapter that would discard the field.
Messages permits `max_tokens: 0` as an explicit cache-population request without
generated output. The request still reserves its complete tokenizable input for
TPM and billing admission, while its output reserve is exactly zero. Routing
requires the `zero_output` deployment capability, currently advertised only by
the native Anthropic adapter, so retry and fallback cannot reinterpret zero as a
default output allowance. Durable Messages batches use the same conversion and
accounting. Chat Completions continues to reject an explicit zero output limit.
Top-level `cache_control` accepts `{ "type": "ephemeral" }` with an optional
`ttl` of `5m` or `1h` and applies the marker to the last cacheable request block.
It shares the four-breakpoint request limit with system, message, tool and client
tool markers. Generation, native token counting and durable Messages batches
preserve the control, and routing requires `prompt_cache`. The control is part
of exact response-cache identity, while semantic response caching is disabled.
`inference_geo` accepts `global` or `us` and routes only to a native deployment
advertising that capability. Generation, native token counting and durable
Messages batches preserve the value. JSON and SSE responses must report the
requested geography; a missing, unknown or changed value fails the response.
Exact and semantic response caches are bypassed for these requests so a prior
response cannot cross the requested processing boundary. For `us`, the model
catalog input and output token prices are multiplied by 1.1 before budget reserve;
the resulting price snapshot is reused at settlement. RPM and TPM limits remain
shared between geography values. Model-version eligibility is enforced by the
upstream, whose terminal validation error is returned without incompatible
adapter fallback.
`context_management.edits` supports server-side `clear_tool_uses_20250919` and
`clear_thinking_20251015`. A request contains one or both strategies; when both
are present, thinking clearing must be first. Trigger, keep and minimum-clearing
thresholds are positive and bounded, tool-name lists are unique and bounded,
and unknown fields fail before routing. Native generation, token counting and
durable Messages batches preserve the configuration and automatically enable
the required provider feature. JSON and SSE responses retain only validated
applied-edit counters for strategies present in the request. Token counting
returns `context_management.original_input_tokens` when reported, and rejects a
value smaller than the post-edit count. The router requires an explicit
`context_management` capability and bypasses exact and semantic response caches
because the effective prompt may differ from the submitted history. TPM admission
remains conservative over the complete submitted context; billing settles from
the ordinary provider-reported usage after editing.
The provider-assigned `standard`, `priority` or `batch` service tier is retained
in JSON and SSE usage. Unknown reported tiers fail the response instead of being
accepted as trusted accounting metadata. Provider-reported `output_tokens_details.thinking_tokens` is retained in JSON,
SSE final usage, and the internal response usage presented to billing settlement;
the total output-token charge remains unchanged.
Token counting is a separate endpoint; background jobs are not supported.

Messages requests may load 1–20 native skills with
`container.skills`. Each reference has `type=anthropic|custom`, a bounded
`skill_id`, and an optional pinned `version`. The request must also include the
native `{type: code_execution_20250825, name: code_execution}` tool. Custom
skills are resolved against the authenticated credential+user owner key before
inference and bind the request to the deployment where the skill was created;
custom skills from different deployments fail before provider execution.
Authorization checks both `skill:<skill_id>` and `code_execution` tool grants.
The container descriptor and code-execution result blocks returned by the
provider are bounded and preserved, including `pause_turn`. Provider-reported
code-execution counts settle `tool_requests` in billing, while the complete
container reference contributes to TPM reserve. Exact and semantic caches are
disabled because execution may create files or mutate managed container state.

The `container.id` returned by a successful skill execution may be supplied with
the same bounded skills list in a later request. The gateway persists the
provider-reported expiry and deployment affinity in PostgreSQL, resolves it with
the authenticated credential+user owner key, and rejects unknown, foreign or
expired IDs before provider execution. Continuations remain pinned to the
original deployment even when routing configuration changes. Each successful
continuation refreshes only the provider-reported expiry for the same owner and
deployment; identifier collisions fail closed. Expired bindings are removed in
bounded batches during writes.

The same request shape is accepted in durable `/v1/messages` batches and is
revalidated when the item executes, including durable container ownership and
affinity. `stream=true` returns a valid Messages SSE sequence, buffered until
the provider response has supplied a valid container descriptor and its owner,
expiry and deployment binding have been stored. A persistence or descriptor
failure is returned before SSE starts, so an unusable container ID is never
published. This mode preserves correctness and failure atomicity; it does not
provide incremental provider latency.

Messages supports one native regex or BM25 tool-search server tool per request.
Function tools may set `defer_loading=true` only when tool search is present and
cannot combine it with a prompt-cache breakpoint. Requests require the explicit
`tool_search` deployment capability and `tool_search` tool grant, include the
native configuration and continuation blocks in TPM reserve, and bypass exact
and semantic caches. Bounded `server_tool_use` and `tool_search_tool_result`
blocks retain provider order across a later assistant-history turn. Tool search
does not create a separate usage unit; its input and output remain part of token
billing. Messages streaming uses the existing bounded buffered conversion so
policy checks finish before native SSE is emitted.

Messages accepts the 2026 dynamic-filtering versions of native web search and
web fetch, including bounded caller and domain controls. Cache bypass is limited
to `web_fetch_20260309` and later; response inclusion is limited to the 20260318
variants. Native flat search location fields are forwarded directly, while the
former nested gateway shape remains accepted for existing clients. Every native
server-tool definition now contributes to TPM and budget reserve.

Managed code execution accepts the 20250825, 20260120 and 20260521 versions.
The selected version is preserved through generation and native token counting;
tool authorization, exact provider usage settlement, cache exclusion, bounded
result validation and skill-container requirements remain identical.

Messages also accepts provider-defined client tools for memory
(`memory_20250818`), bash (`bash_20250124`) and text editing
(`text_editor_20250124` or `text_editor_20250728`). The gateway validates the
version-specific fixed name and forwards only the documented configuration; the
calling application executes each returned `tool_use` and sends a `tool_result`
continuation. The gateway does not execute shell, filesystem or memory commands.
Deployments must advertise the matching `memory_tool`, `bash_tool` or
`text_editor_tool` capability, and access policy must grant the fixed tool name.
Definitions contribute to TPM and token-count requests. Exact and semantic
response caches are disabled so a previously generated action is never replayed.
The newer text editor accepts `max_characters` from 1 through 1,048,576. Deferred
loading requires tool search and cannot share a prompt-cache breakpoint.

The `computer_toolset_20260801` entry enables the current 17-member desktop
interaction contract. It has no `name`; optional `configs` may enable or disable
fixed members and may defer all enabled members together when a tool-search tool
is present. `allowed_callers`, when supplied, is exactly `["direct"]`.
Authorization evaluates every enabled member as `computer:<member>`. The
`computer_toolset` deployment capability is required. Assistant `tool_use` and
user `tool_result` blocks must echo `toolset_name: "computer"`; results accept
bounded text and base64 image content, including explicit error results. These
requests bypass response caches, and the gateway reserves 4,590 native input
tokens for the full definition in TPM and budget admission while native token
counting forwards the exact toolset entry upstream.

The `browser_toolset_20260801` entry enables 27 browser actions by default.
`file_upload`, `read_console`, `read_network` and `javascript_exec` remain
disabled unless explicitly enabled in `configs`; every enabled member must use
the same deferred-loading setting. Authorization evaluates enabled actions as
`browser:<member>`, independently from computer actions with the same name, and
routing requires `browser_toolset` on a native Anthropic deployment. The
gateway validates bounded text and image results plus `browser_state`: at most
100 unique tabs, exactly one active tab in a non-empty inventory, at most 200
state changes, safe rendered strings, valid tab-open and download events, and
the exact state-only result required by tab-management actions. Error results
cannot carry browser state. Native history, JSON/SSE output, batches and token
counting retain `toolset_name: "browser"`. Response caches are bypassed. TPM
and budget admission reserve 6,670 input tokens for the default definition and
7,550 when any opt-in member is enabled; exact provider usage settles billing.

Regressions cover request/response conversion, native and fallback SSE, stream
failure, model/tool authorization, TPM, unknown input, response-size bounds and
reported usage reaching the accounting stage through Router. No live paid
provider calls were used. Protocol references:
[Messages](https://platform.claude.com/docs/en/api/messages/create) and
[streaming](https://platform.claude.com/docs/en/build-with-claude/streaming), and
[extended thinking](https://platform.claude.com/docs/en/build-with-claude/extended-thinking).

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
images, function schemas, function-call history/results, tool choice and native
skill execution context. Skill references, the managed code-execution tool and
an optional reusable container ID are sent through the native counter with the
required provider capability header. The counter wire request contains no
generation limit or streaming flag. Unsupported context parts and parameters fail
before HTTP. Results retain the provider model and source; errors never fall back
silently to the local context estimate.

The adapter enforces a 30-second context deadline, the inference body limit and
a 64 KiB response limit. Redirects are refused to protect the provider API key;
missing, negative, fractional or overflowing counts are errors. Caller
cancellation is propagated. The counter is not a replacement for reserve estimates.

`POST /v1/messages/count_tokens` exposes the counter with the same native auth
headers as Messages. It accepts model, messages, system, tools, tool_choice,
output_config and the same bounded `container.skills` shape as message creation;
generation parameters are rejected. Custom skills and reusable container IDs are
resolved using the authenticated credential+user owner key, pin counting to the
same deployment, and pass `skill:<id>` plus `code_execution` tool ACLs. Partial
assistant/tool-use history is allowed for counting. Advanced block types remain
unsupported.

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
placed in the URL. With `gcp_adc`, the same operation uses a metadata-issued bearer
token instead. Results use totalTokens as the counted input, including cached
context, without creating generation usage or altering reserve estimation.

The same 30-second deadline, inference request-body limit, 64 KiB response limit,
redirect refusal and invalid-count checks apply. Gemini-specific unsupported
controls still fail before HTTP. Tests cover complete native context, model alias
routing, malformed counts, redirect refusal and cancellation.
Protocol: [Gemini token counting](https://ai.google.dev/api/tokens).

Bedrock implements the same internal counter contract with native
`POST /model/{model}/count-tokens`. The request uses the resolved upstream model
and wraps the complete native Converse context under `input.converse`, including
system messages and tool schemas. Bearer and SigV4 modes reuse the inference
credential chain and signing rules. The response must contain a nonnegative
`inputTokens`; body size, response size, deadline and redirect limits match the
other native counters. The public gateway flow still performs policy and quota
admission without generation billing.
Protocol: [Bedrock CountTokens](https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_CountTokens.html).

The synchronous Converse ingress also accepts native `image` content blocks in
user messages for JPEG, PNG, GIF and WebP. Image bytes are decoded, signature
checked and bounded by the shared per-image, total-image and request limits before
authorization reaches a provider. The normalized image remains a media block for
DLP/AV projection and requires a vision-capable deployment; the native adapter
then reconstructs the original ordered Converse image block. Images in assistant,
system and tool-result content, remote URLs and document blocks are rejected.

## Native GenerateContent API

POST `/v1beta/models/{model}:generateContent` returns native JSON; POST
`/v1beta/models/{model}:streamGenerateContent?alt=sse` returns native SSE.
The action is parsed from the final colon, so versioned model IDs such as
`phi3:latest` remain addressable on generation, streaming and token-count routes.
Send a gateway key in `x-goog-api-key` or Bearer authorization. Query credentials
and conflicting authentication headers are rejected. Both endpoints use the shared
Chat pipeline for authorization, model/tool ACL, quotas, content policy and billing.

The GenerateContent converter maps native system/contents, inline user
images, function declarations and results, tool choice, output limits,
temperature/top-p/seed, stop sequences and JSON output configuration to Chat.
Top-level `serviceTier` accepts `unspecified`, `standard`, `flex` or `priority`
and `usageMetadata.serviceTier` reports the effective provider tier in JSON and
SSE. Top-level `store` is forwarded exactly, including an explicit `false`.
`store=true` bypasses exact and semantic response caches so every accepted
request reaches the provider and its storage side effect is not skipped.
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
from candidate tokens, while billing includes both. Provider-reported
`toolUsePromptTokenCount` is added to billable input tokens and preserved as a
separate native usage field; overflow and totals below the complete declared
input/output usage fail closed. The Gemini adapter preserves
upstream modelVersion when provided; otherwise the normalized model name is used.
SSE emits text incrementally and buffers function calls until their arguments are
complete JSON objects. Frame and tool accumulation have separate 32 MiB limits.
Stream failures emit a redacted native error without a successful finish reason.

Regression tests cover conversion, JSON/SSE, authorization, quotas, native usage
and billing, bounded stream accumulation and disconnects. `safetySettings` accepts
one validated threshold per supported harm category, requires an explicit
`gemini_safety_settings` deployment capability, contributes to input TPM reserve
and bypasses exact and semantic response caches. A native `googleSearch: {}` tool
requires the `web_search` capability, forwards Gemini Google Search grounding,
validates and preserves source/support/search-entry metadata, bills the actual
returned search-query count, and bypasses response caches. Search context, location
and per-request use controls are rejected because the native tool cannot represent
them. A native `codeExecution: {}` tool requires the `gemini_code_execution`
deployment capability and the `code_execution` credential/access-group tool grant.
It preserves bounded `executableCode` and `codeExecutionResult` parts in JSON and
SSE, includes returned usage in normal billing, includes supplied execution history
in TPM estimates and bypasses exact and semantic response caches. Only the native
provider runtime executes the generated Python; the gateway does not run that code.
A native `urlContext: {}` tool requires the `url_context` deployment capability
and the matching credential/access-group tool grant. It forwards provider-side URL
retrieval without gateway-side fetching, includes the declaration in the TPM
estimate, includes reported tool-input tokens in billing and bypasses exact and
semantic response caches. JSON and SSE preserve at most 20 validated
`urlContextMetadata.urlMetadata` entries; malformed URLs, unknown retrieval statuses,
unknown fields and oversized metadata fail closed. Native token counting applies the
same tool ACL. Cached content, other server tools, file parts and other
audio formats remain unsupported.

A native `googleMaps: {}` tool requires the `google_maps` deployment capability
and matching tool grant. `toolConfig.retrievalConfig.latLng` is optional when the
tool is enabled; when present, both coordinates are required and validated before
provider execution. The tool declaration and location enter TPM estimation. Exact
and semantic response caches are bypassed. A provider response that confirms Maps
use through bounded place or widget metadata settles one grounding request, while
web-search queries remain separately counted. JSON and SSE preserve the validated
metadata and derive ordinary URL citations from Maps grounding supports. Native
token counting applies the same tool grant.

GenerateContent accepts inline WAV, MP3/MPEG, AIFF, AAC, OGG/Opus, FLAC, M4A
and WebM audio parts in user content. Audio is size- and container-signature
validated, projected to configured AV scanners, included in the conservative TPM
reserve, routed only to deployments with `audio_input`, and excluded from exact
and semantic response caches. Raw L16, A-law and mu-law remain unsupported because
the native part has no sample-rate/channel fields from which the gateway could
validate and account for headerless bytes. Provider file references remain
unsupported.

Inline and owner-scoped stored video accepts MP4, WebM, MPEG, MPG, MOV, AVI,
FLV, WMV and 3GPP. The declared MIME type must match a bounded container or
stream signature before policy execution. Video bytes enter AV projection and
the conservative TPM estimate, require `video_input`, and bypass exact and
semantic response caches.


### Native Gemini file search

`tools[].fileSearch` performs retrieval against provider-managed stores. The request
accepts one to 20 unique `fileSearchStores/...` names, an optional bounded metadata
filter, and optional `topK` from 1 to 100. File search cannot be combined with other
tools. Routing requires `gemini_file_search`; authorization requires `file_search`
and `gemini_file_search:<store-name>` for every requested store. The tool definition
is included in the TPM and budget reserve, native token counting preserves it, and
exact and semantic response caches are bypassed. Returned retrieval grounding is
bounded and validated before citations and raw native metadata are exposed.

### Native Gemini computer use

`tools[].computerUse` enables client-executed browser, mobile, or desktop action
loops. Configuration validates the environment, excluded predefined functions,
prompt-injection detection flag and safety-policy overrides. Routing requires
`gemini_computer_use`. Authorization requires `computer_use`, an environment grant
(`gemini_computer_use:<environment>`) and a dedicated grant for every disabled safety
policy. Tool configuration enters token reserve and native token counting, while exact
and semantic response caches are bypassed. Function-call arguments, including provider
safety decisions, remain in the native GenerateContent continuation contract.

### Native Gemini MCP servers

Native GenerateContent requests may select up to eight configured MCP servers with `tools[].mcpServers[].name`. The name is a gateway registry ID; client-supplied URLs and headers are rejected. Resolution happens after authentication, and every server must be enabled, use Streamable HTTP, explicitly allow native provider execution, and pass the effective connector ACL.

The registry opt-in controls whether the configured HTTPS URL and encrypted bearer credential may be sent to the selected model provider. These requests require the `gemini_mcp` deployment capability, bypass exact and semantic response caches, and preserve the resolved server configuration for native token counting. Provider-reported prompt and tool-input tokens enter normal billing; the provider API does not expose a separate MCP-call counter, so the gateway does not synthesize one.

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

OpenAI-compatible embeddings accept a token-ID array as one input or up to 2048
token-ID arrays as independent inputs. Each token array is nonempty and bounded
to 8192 nonnegative 32-bit IDs; the aggregate limit is 300000 tokens. TPM and
billing reserve use the exact number of IDs rather than serialized JSON size.
Native adapters that accept text only reject token inputs before policy modules
or provider calls. A route requiring DLP also rejects opaque token IDs because
the gateway cannot reconstruct trustworthy text for content inspection.

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

This is buffered replay rather than live token streaming. Background requests use
the separate durable lifecycle described below.
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
not change provider-specific error handling.
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
input, output, total, input/output cached, cache-write, and cache-creation token counters.
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

### Responses server-side compaction

Responses accepts one optional `context_management` entry with
`type="compaction"` and an optional positive `compact_threshold`. Compatible
JSON and SSE adapters preserve the native object. The gateway validates the
shape before routing and exposes support in the provider parameter profile;
native adapters that cannot preserve the setting return an explicit
`unsupported_parameter` error.

Because compaction can emit opaque state and depends on the provider's current
context, these requests bypass gateway response caches and are not
copied to shadow deployments. Provider-reported terminal usage remains the
authoritative billing settlement.

### Responses provider moderation

Responses accepts an optional provider-side `moderation` object with a required
model and optional input/output policy modes. Each mode is validated as `score`
or `block`. Compatible JSON and SSE adapters preserve this object, while native
adapters that cannot represent it return `unsupported_parameter`.

Provider moderation supplements the gateway's effective content policy; it
does not disable or replace gateway DLP, AV or output checks. These requests
bypass gateway response caches and shadow execution because the provider's
moderation policy can change independently. Terminal provider usage remains
authoritative for billing.

### Responses prompt-cache prewarming

Responses accepts `prompt_cache_options.prewarm=true` on compatible adapters to
prepare provider prompt caches without generating output. Chat requests reject
this Responses-only control. Prewarm requests reserve their estimated input
tokens with zero output tokens, then settle from exact terminal provider usage.

Prewarming always executes against the selected provider, so it bypasses the
gateway response cache and shadow execution. `prewarm=false` is preserved as an
explicit provider setting and follows the normal response path.

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

`store` controls upstream response storage. An explicit `true` also enables the
gateway ownership binding required by `GET /v1/responses/{id}`. Gateway logging
policies remain separate; this option is not a gateway-wide retention switch.
Other background lifecycle endpoints are not published.

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

Responses usage now retains optional `output_tokens_details.reasoning_tokens` and
`output_tokens_details.cached_tokens`, using the existing completion-token detail
type, plus compatible `input_tokens_details.reasoning_tokens`. Negative values
are rejected before forwarding a native SSE event or returning decoded JSON.
The provider-assigned `service_tier` is retained in JSON and terminal SSE
responses so callers can verify the tier that actually served the request.
Compatible Responses also retains nonnegative provider-reported
`num_sources_used` and `num_server_side_tools_used`, including explicit zeroes.
These observability counters remain separate from token totals and local tool
billing dimensions.
Provider-reported top-level `citations` are retained as at most 1024 bounded
HTTP(S) source URLs in JSON and terminal SSE responses. Invalid citations fail
before the terminal response is delivered.
This detail is not added to `output_tokens` or `total_tokens`; those reported
counters remain unchanged. Tests cover positive/zero details, negative rejection
and unchanged totals for JSON and native streaming. The OpenAPI response schema
includes the optional detail object. As with existing token detail types,
zero-valued members may be omitted when serialized while retaining the detail
object.

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

Responses enforces the documented positive minimum for supplied
`max_output_tokens` and the legacy `max_tokens` alias. Zero and negative values
return `400 invalid_request` at the HTTP boundary and router before execution.
Omission or null retains default reserve behavior; positive limits retain the
existing reserve calculation.

Responses accepts at most one non-null output limit: `max_output_tokens` or the
legacy `max_tokens` alias. Supplying both now returns `400 invalid_request`, even
when their values match. Previously the gateway reserved against max_output_tokens
but native-compatible adapters forwarded both fields, leaving provider precedence
ambiguous. Clients sending both must remove one. Single-limit requests and null
aliases retain their existing behavior.

Native Responses adapters normalize the gateway's legacy `max_tokens` alias to
`max_output_tokens` before sending JSON or streaming requests. They never send
`max_tokens` to the native Responses endpoint. An omitted cap remains omitted,
and the forwarded value matches the output cap selected for token reservation.
This changes the upstream wire field for clients using the legacy alias while
preserving their configured token limit.

Responses accepts string-valued `metadata` and forwards it through native
OpenAI-compatible and Ollama JSON/SSE requests. Upstream response metadata is
retained in JSON, assembled SSE and synthetic SSE snapshots. An explicit metadata
snapshot replaces earlier metadata rather than merging stale keys. The upstream
may apply additional metadata restrictions. The gateway enforces at most 16
entries, 64 Unicode code points per key and 512 per value before execution. Anthropic conversion and demo reject nonempty metadata
with `400 unsupported_parameter`; it is not silently mapped to unrelated native
metadata semantics. Gateway authorization and billing identities are not derived
from this client-supplied object.

Responses also accepts `safety_identifier` with a maximum of 64 Unicode
characters. OpenAI-compatible JSON and streaming requests forward it unchanged.
The identifier participates in exact and semantic cache keys but never replaces
authenticated user or credential identity in billing. Anthropic, Ollama and demo
Responses adapters reject it explicitly when they cannot preserve its meaning.

`prompt_cache_key` is forwarded unchanged by the OpenAI-compatible Chat and
Responses adapters in JSON and streaming requests. It scopes exact and semantic
gateway cache entries so an explicit upstream cache partition is not bypassed by
local reuse. It never replaces authenticated user or credential identity in
billing. Native adapters return `400 unsupported_parameter` instead of silently
discarding the key.

Chat `prompt_cache_options` accepts `mode=implicit|explicit` and `ttl=30m`.
OpenAI-compatible JSON and streaming requests preserve the object. Exact and
semantic gateway cache keys include it, so requests with different upstream
prompt-cache policies do not share a cached response. Native adapters reject the
object explicitly when they cannot represent its semantics.

Chat `prompt_cache_retention` accepts `in_memory` or `24h` and is forwarded by
the OpenAI-compatible adapter in JSON and streaming requests. It is included in
both gateway cache scopes, while native adapters reject it explicitly. This field
controls the selected provider's prompt-prefix cache retention; gateway response
cache retention remains governed by the effective gateway cache policy.

Chat text content parts may mark up to four exact prefix boundaries with
`prompt_cache_breakpoint: {"mode":"explicit"}`. The gateway validates the
placement and count before authorization modules, preserves the marker through
text anonymization, and forwards it only to compatible adapters. Native adapters
reject it rather than silently dropping the boundary. Exact cache keys retain the
full request; semantic response reuse is disabled because it would bypass the
requested provider prefix-cache boundary.

Chat and Responses usage preserve provider-reported modality and predicted-output
breakdowns: input/prompt `audio_tokens`, `image_tokens`, `text_tokens`, and output
`accepted_prediction_tokens`, `rejected_prediction_tokens`, `audio_tokens`,
`cached_tokens`, `reasoning_tokens`, `text_tokens`. Negative detail counts are rejected before a
JSON response or SSE event is delivered. Billing continues to settle from the
provider's aggregate input/output counts, which already include rejected predicted
tokens, so detail fields are observability data and are not added a second time.

Chat `prediction` accepts static content as a string or as strict `text` parts,
including an optional explicit prompt-cache breakpoint. OpenAI-compatible JSON
and streaming requests forward it unchanged. Exact and semantic caches include
the prediction, and native adapters return `400 unsupported_parameter` when they
cannot preserve predicted-output behavior. Accepted and rejected prediction token
details remain part of the aggregate provider completion count and are never added
again during billing settlement.

Chat `verbosity` and Responses `text.verbosity` accept `low`, `medium` or `high`.
OpenAI-compatible JSON and streaming requests preserve the supplied value.
Exact and semantic cache keys include it because verbosity changes output
semantics. Native adapters reject it explicitly before execution.

OpenAI-compatible Chat responses preserve the upstream `created`, `metadata`,
`service_tier` and `system_fingerprint` envelope in JSON and in responses
assembled from SSE. The stream collector rejects a provider stream that changes
one of these identities between events. Synthetic SSE reuses the collected
upstream timestamp and envelope for every generated event; when `created` is
absent, it selects one timestamp for the whole synthetic stream.

Chat accepts string-valued `metadata` with the same 16-entry, 64-character key
and 512-character value limits as Responses. Compatible JSON and SSE requests
forward it unchanged. `store=false` is also forwarded explicitly. `store=true`
is rejected before execution until the gateway exposes the corresponding stored
Chat lifecycle; accepting it would create resources that gateway clients cannot
retrieve or delete. Native adapters reject both controls when their contracts
cannot preserve them. Both fields participate in cache scope, while authenticated
billing identity remains independent of client metadata.

Chat и Responses распознают `service_tier` и проверяют известные значения.
Managed OpenAI передает `auto`, `default`, `flex` и `priority`; OpenRouter
передает значения из общего валидируемого набора. Generic compatible и Azure
отклоняют поле до подтверждения конкретного upstream-контракта. Native Anthropic Chat и inbound
Messages принимают `auto` и `standard_only`; остальные native adapters возвращают
`400 unsupported_parameter` до provider modules, TPM, cache, billing и upstream.
Назначенный Anthropic tier сохраняется в Chat envelope и native Messages usage.

### Native prompt caching

Chat text parts and function tools may declare an explicit
`prompt_cache_breakpoint` with `mode=explicit` and optional `ttl=5m|1h`.
The Messages API maps `cache_control: {type: ephemeral}` on system text,
message text and tools to the same internal contract. A request may contain at
most four breakpoints across messages and tools. Breakpoints participate in
the serialized token reserve and exact-cache key and disable semantic caching.

The native Anthropic adapter emits the provider `cache_control` object without
changing the requested TTL. Other native adapters reject the parameter.
Routing requires an explicit `prompt_cache` deployment and model capability,
so a request cannot reach an adapter that would discard the breakpoint.
Provider-reported cache-read and cache-creation tokens continue through usage
normalization and billing settlement.

### Stored Responses lifecycle

The native-compatible adapter exposes bounded retrieval transport. It performs a
GET using the configured provider credential, accepts
only bounded ASCII resource IDs, rejects redirects, observes context cancellation,
and uses the bounded Responses decoder and redacted upstream error conversion.
A successful payload must return the requested ID. Reading reported usage here
does not execute inference or run billing settlement.

Lifecycle ownership has a separate storage foundation from routing affinity.
Records are scoped by gateway credential ID, user ID and response ID, with model,
endpoint and a hash of deployment name/provider/type/base URL/credential identity.
Records are bounded to 4 KiB, use an explicit TTL and require a configured shared
SessionStore; absence, corrupt data and storage failures do not permit an upstream
lookup. Backend error details are not returned to callers.

Ownership persistence now requires atomic create-or-equal storage. Redis executes
comparison and insertion in one Lua operation. An identical retry succeeds without
refreshing TTL; a different model/deployment binding for the same scoped response
ID returns an ownership conflict and preserves the original record. The ownership
store no longer accepts a backend providing only unconditional Set.

Creation now persists this binding for an explicit `store=true` request. Such a
request requires a configured Redis-backed ownership store and an authenticated
gateway credential before provider execution. It bypasses exact response caching
and shadow mirroring so a stored resource cannot be substituted or duplicated.
After a successful provider call, post-response accounting completes before the
binding is written. A storage failure returns `503 response_ownership_unavailable`;
an ID collision with a different binding returns `409 response_ownership_conflict`.
Omitted, null or false `store` values retain the existing stateless behavior.

`background=true` additionally requires `stream=false`, PostgreSQL async-job
storage and an explicit `background_responses` deployment capability. A queued or
in-progress upstream response persists an owner-scoped job before success is
returned. The job contains lifecycle and billing identifiers but never the prompt,
provider credential or response content. Billing reserve is not committed with
zero usage at creation time.

Gateway replicas claim jobs with PostgreSQL `SKIP LOCKED` leases and fencing
generations. They retrieve the resource only through its immutable ownership
binding, retry nonterminal states with bounded backoff, and commit actual terminal
usage with the original internal execution ID. A process restart or lost lease can
repeat settlement safely through billing idempotency; stale workers cannot delete
or reschedule a newer lease. Storage failure after upstream creation triggers an
upstream cancellation attempt, removes the ownership binding and cancels the
billing reserve before the gateway returns an error.

`GET /v1/responses/{id}` runs authentication without opening a generation billing
lifecycle, resolves the record within the credential and user scope, and rechecks
current model, tag, access-group and RPM policy before reading upstream. The router
loads the immutable binding again immediately before transport and rejects a
removed or changed deployment with `409 response_deployment_changed`. Missing and
cross-owner records return the same `404 response_not_found` response. Retention,
upstream model aliases and reconstruction after restart remain lifecycle integration
requirements; optional affinity alone is not sufficient.

`POST /v1/responses/{id}/cancel` applies the same ownership, deployment and current
policy checks before forwarding a bounded cancellation request to the original
native-compatible provider. It uses the deployment admission, timeout, retry,
circuit and telemetry controls without opening a new generation billing lifecycle.
The ownership record remains available for subsequent retrieval of the terminal
resource state.

`GET /v1/responses/{id}/input_items` applies the same lifecycle authorization and
accepts only bounded `after`, `limit`, `order` and repeated `include` parameters.
The upstream JSON body is capped at 32 MiB and 10,000 items. Items remain raw JSON
objects so newly introduced provider fields are not silently discarded. Listing
does not open a generation billing lifecycle.

`DELETE /v1/responses/{id}` removes the upstream resource before deleting its
ownership binding. Redis compare-and-delete prevents a stale cleanup from removing
a different immutable record. If Redis cleanup fails after upstream success, the
gateway returns `503 response_ownership_unavailable` and retains the binding; a
retry treats upstream 404 as the desired deleted state and retries atomic cleanup.
Once cleanup succeeds, later requests return `404 response_not_found` without an
upstream call. Deletion does not open a generation billing lifecycle.
Provider type `azure-openai` добавляет `/openai/v1` к resource-root URL и сохраняет явно настроенный path, включая `/openai/deployments/{deployment}` для versioned data plane. Непустой `api_version` передается ровно один раз как query parameter `api-version` во всех versioned HTTP operations. Realtime независимо строит native GA или preview WebSocket URL и не смешивает параметры этих контрактов. `auth_type=api_key` использует header `api-key`; `auth_type=entra` использует статический bearer token из write-only credential vault либо, при отсутствии credential, AKS projected-token federation, App Service/Container Apps managed identity или VM IMDS. Provider endpoints с официальным Azure US Government suffix автоматически используют `login.microsoftonline.us` и `cognitiveservices.azure.us`; Azure China suffix выбирает `login.chinacloudapi.cn` и `cognitiveservices.azure.cn`. Остальные endpoints используют public-cloud authority и audience. Временные tokens обновляются до истечения срока, параллельные refresh объединяются. Redirects запрещены, чтобы credential не мог перейти на другой origin. Discovery использует тот же authentication contract; для versioned deployment path оно выполняется через resource-level `/openai/models`.

## Vector stores

`POST /v1/vector_stores`, `GET /v1/vector_stores`, and the owned `GET`,
`POST`, and `DELETE /v1/vector_stores/{id}` lifecycle persist store metadata in
PostgreSQL. Ownership combines the authenticated credential and user, so another
user of the same credential cannot enumerate or address the resource. Creation
uses an atomic per-owner quota configured by `VECTOR_STORE_OWNER_QUOTA`; list
pagination accepts `after` and a limit from 1 to 100. Expiry uses the PostgreSQL
clock and supports only `last_active_at` with 1 to 365 days. File ingestion and
vector search are separate operations and are not reported as available by this
metadata lifecycle.

Attaching a file applies both the atomic per-store file-count limit and the
overflow-safe aggregate byte limit configured by `VECTOR_STORE_FILE_QUOTA` and
`VECTOR_STORE_BYTE_QUOTA`. Each attachment can persist up to 16 string, finite
number, or boolean attributes. Keys are limited to 64 characters and string
values to 512 characters.
The optional `chunking_strategy` accepts `{"type":"auto"}` or a static
strategy with `max_chunk_size_tokens` from 100 to 4096 and
`chunk_overlap_tokens` no greater than half the chunk size. The selected policy
is persisted per attachment and is applied consistently by parsed-content
retrieval and synchronous vector search. Static boundaries use the gateway's
conservative context-token estimator, so they are stable across deployments but
may leave more unused space than a provider-specific tokenizer. Invalid token
ranges or overlap greater than half the chunk size return `400`.
Expired source files do not consume either limit.
`POST /v1/vector_stores/{id}/files/{file_id}` atomically replaces the complete
attribute map for an owned attachment; an empty object clears it.
The attachment list accepts one of `after` or `before`, a limit from 1 to 100,
`order=asc|desc`, and `filter=in_progress|completed|failed|cancelled`. Cursor
lookup, status filtering, and ordering run in PostgreSQL using `created_at` plus
the file ID as a stable tie-breaker.

`POST /v1/vector_stores/{id}/file_batches` atomically attaches 1 to 2000 owned,
non-expired files. The request accepts either `file_ids` with shared attributes
and chunking policy, or `files` with per-file options. These forms cannot be
combined. All ownership, duplicate, count-quota and overflow-safe byte-quota
checks run in one PostgreSQL transaction, so a failed batch leaves no attachment
or batch record. Successful batches complete synchronously and remain available
through `GET /v1/vector_stores/{id}/file_batches/{batch_id}` and the paginated
`GET .../{batch_id}/files` collection. Because there is no background ingestion
phase, `POST .../{batch_id}/cancel` is an idempotent retrieval of the completed
terminal state.

`GET /v1/vector_stores/{id}/files/{file_id}/content` verifies the owner-scoped
attachment before reading the source file. It returns at most 100 Unicode-safe
text chunks from up to 1 MiB of `purpose=assistants` UTF-8 text, Markdown, CSV,
or JSON content. Missing source content, unsupported media, invalid UTF-8, NUL
bytes, empty text, and larger files fail without exposing another owner's file.

`POST /v1/vector_stores/{id}/search` provides bounded semantic retrieval for an
owned store. It accepts an explicit authorized embedding model and optional
attribute filters. The original string map requires every key and value to match.
Typed filters support `eq`, `ne`, `in`, `nin`, numeric `gt`, `gte`, `lt`, `lte`,
and nested `and`/`or`. Trees are limited to four levels, 32 nodes and 16 children
per compound or membership expression. Filtering happens on owner-scoped
attachment metadata before file content is read. The
request accepts a store with at most 20 attachments, then loads matching
`purpose=assistants` UTF-8 text, Markdown, CSV, or JSON files and at
most 1 MiB of content, then splits them into at most 100 Unicode-safe chunks.
The query and chunks enter the normal content-policy and token/budget admission
path together. One provider embedding batch is settled from reported usage, and
the gateway validates dimensions, indices and finite values before cosine
ranking. Stores above the synchronous limits fail before an upstream request;
durable ingestion and indexing remain unavailable.
