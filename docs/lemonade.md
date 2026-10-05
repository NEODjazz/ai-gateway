# Lemonade provider

AI Gateway accepts `type: lemonade` in static provider configuration and managed
Providers. This is an additive provider type; existing provider configuration and
public inference URLs remain unchanged. The adapter uses Lemonade's OpenAI wire
API and optional bearer credentials for an authenticated server or reverse proxy.

## Configure through UI

1. Open **Models & endpoints → Providers**, create a provider and select
   **lemonade**. Use the server URL, normally `http://localhost:13305` or
   `http://localhost:13305/v1` when Gateway runs on the same host. A Gateway pod
   needs a hostname reachable from the pod, such as `host.docker.internal` in a
   local Rancher Desktop environment; pod `localhost` refers to the pod itself.
2. Create a credential only if the server/proxy requires authentication.
3. Open **Model onboarding**, choose this provider and **Test & discover models**.
   Discovery uses `/v1/models` without `show_all=true`; it does not install,
   download, load, or delete models.
4. Review the discovered capabilities, public model name, upstream model and
   pricing, then apply the plan. Choose an appropriate deployment request timeout
   for model loading and inference. The adapter HTTP client has a 180 second
   limit for the full upstream call.

Root URLs, URLs ending in `/v1`, and reverse-proxy path prefixes are supported.
Query strings, fragments and credentials embedded in the URL are rejected.

Static configuration example:

```json
{
  "name": "lemonade-local",
  "type": "lemonade",
  "base_url": "http://host.docker.internal:13305",
  "models": ["Qwen3-0.6B-GGUF"],
  "capabilities": ["chat", "responses", "stream"],
  "stream": true
}
```

## Operations and model capabilities

| Gateway operation | Native contract |
| --- | --- |
| Chat Completions | JSON and SSE, function tools, vision/audio inputs when the model supports them |
| Completions | JSON and SSE for text models |
| Responses | Stateless JSON and SSE on compatible backends |
| Embeddings | Text or text arrays; float/base64 output |
| Rerank | Text query and text documents |
| Images | Generation, one-image editing with optional mask, variations; base64 output |
| Transcription | Buffered WAV input; JSON/verbose JSON output |
| Speech | Buffered audio; WAV/MP3/Opus where the selected backend supports the format |

The provider profile describes the adapter's possible operations, not a promise
that every installed model supports them. Discovery uses deployment-mode labels
and recipe defaults independently of characteristic labels. An embedding or
reranking model is not offered as a chat model. `reasoning` and `vision` alone do
not establish a chat mode. Conflicting modes, modes incompatible with known
recipes, invalid IDs and malformed metadata fail discovery. Unknown recipes
without a mode remain without automatically selected capabilities.

`cloud` and `flm` models support Chat but explicitly reject Responses. Responses
are discovered for `llamacpp`, `llamacpp-hrx`, `ryzenai-llm`, `vllm` and `ds4`.
Tool, vision and chat-audio capabilities require the corresponding model labels.
An upscaler is not advertised as an image generator. Native Realtime transcription
does not imply support for Gateway's generation Realtime contract.

Parameters outside the implemented native contract are rejected explicitly.
Chat SSE retains the first reported `created` timestamp for the whole response,
including when the native backend timestamps individual chunks separately.
Invalid timestamps and changing stream IDs/models remain errors.
Chat `repetition_penalty` maps to native `repeat_penalty` (range 1–2);
`max_tokens` and `max_completion_tokens` are mutually exclusive. Responses output
limit aliases use `max_output_tokens`. `store: false` is accepted for stateless
requests; stored Responses, continuity IDs, background jobs and managed tools
are unsupported. In Playground leave **Use API session management** off.

Function schemas retain their constraints. Length bounds at or above 2000 and
array-item bounds at or above 2001 are rejected because the native llama.cpp
backend removes them as a grammar workaround. Constraints in schema defaults and
examples are literal annotation data and are not rewritten.

The adapter does not expose daemon management, model downloads, custom 3D/music/
classification/upscaling APIs, native speech streaming, or raw PCM whose sample
rate/channel contract varies by backend. Backend-specific generation extensions
that are absent from Gateway's public API are not silently accepted.

## Documents and accounting

For PDFs use deployment **Document processing (PDF) → docling** with the optional
Docling stack enabled. This converts the document into text before security
checks, context limits and token reservation; it does not add native `file_input`
to Lemonade. See [Document processing](document-processing.md).

Native token usage is retained when available, including rerank input-token
counts. Missing token usage uses Gateway's existing estimated-usage policy rather
than being treated as reported zero. Images without token usage retain their
actual returned count and use catalog `image_cost_per_unit`; no synthetic token
counts are inserted. WAV transcription uses the measured input duration and
catalog `audio_cost_per_minute` when the server omits usage. Invalid or incomplete
reported usage is rejected. Keep model pricing explicit, including a deliberate
zero price for local inference when appropriate.

## Validation reference

Protocol checks use isolated HTTP servers and synthetic fixtures, including URL
prefixes, bearer authentication, streaming, discovery, model aliasing, unsupported
parameters, schema bounds, image counts and WAV duration. No installed model,
developer document or credential is needed for the unit suite.

The native contract was inspected in the official
[API documentation](https://lemonade-server.ai/docs/api/) and
[source revision ee87a42eaba7387fd17ae59b36d3e99239633eae](https://github.com/lemonade-sdk/lemonade/tree/ee87a42eaba7387fd17ae59b36d3e99239633eae).
Different server/backend releases still require a live smoke test before use.

## Responses reasoning content

Responses accepts `reasoning_text` in a reasoning item's `content`, separately
from `summary_text` in `summary` and the assistant message. Both JSON and native
`response.reasoning_text.delta` / `.done` streaming events retain this content.
Buffered streams also emit reasoning text events. This adds a supported content
type to the public Responses contract without changing existing fields or URLs.
Playground displays reasoning separately from the answer. A reasoning-only
response remains reasoning-only; the Gateway does not invent an answer when the
model exhausts its output allowance before producing an assistant message.

Some native streaming backends omit `output_index` on item events. The Lemonade
adapter supplies it from stable item IDs announced in `response.output_item.added`.
Unknown or conflicting identities are rejected; generic provider validation is
unchanged. The mapping is bounded and isolated to each request.
