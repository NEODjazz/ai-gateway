# Lemonade API validation

Validation date: 2026-10-05. Local server: **2026.40.0**, llama.cpp on Metal.
Installed models: `MiniCPM5-1B-GGUF` and `Qwen3.8-27B-GGUF`.

## Evidence and boundaries

The API inventory was reviewed against the official [API reference](https://lemonade-server.ai/docs/api/)
and the six reference pages served by the installed daemon at `/v1/docs`.
The installed documentation is preferable when the public site describes a newer release.

Read-only inspection of Gateway's control-plane database confirmed a managed
Lemonade provider at `http://host.docker.internal:13305` and a MiniCPM deployment
with `chat`, `stream`, `responses`, `tools`, `structured_output`, and a 30-second
request timeout. `/v1/models` was reachable directly from the Gateway pod.
The installed Qwen model was tested through an isolated configuration; it was
not added to the real Gateway deployment.

**24 live checks passed:** 17 inference cases on MiniCPM, five on Qwen, and
discovery for each model. Inference tests run actual public Gateway handlers,
validation, routing, model alias translation, and the Lemonade adapter against
the real daemon. Authentication uses a test fixture; billing uses a recording
post-response hook. These checks do not establish production SSO, persisted
billing, DLP/AV service execution, ingress behavior, or concurrency capacity.
They use synthetic prompts, tool results, and an in-memory PNG, never user files.

## Live inference coverage

| Case | MiniCPM | Qwen |
| --- | --- | --- |
| Chat JSON and SSE | Passed | Passed |
| Chat sampling controls and output token aliases | Passed | Not run |
| Chat structured JSON schema; returned content matches the schema | Passed | Not run |
| Legacy Completions JSON and SSE | Passed | Not run |
| Stateless Responses JSON and SSE | Passed | Passed |
| Responses message-array input | Passed | Not run |
| Gateway's inbound Messages-to-Chat bridge, JSON and SSE | Passed | Not run |
| Forced function calls, valid arguments, Chat JSON and SSE | Passed | Not run |
| Forced function calls, valid arguments, Responses JSON and SSE | Passed | Not run |
| Client-supplied function-call history and result, Chat and Responses | Passed | Not run |
| Chat with a synthetic PNG | Not a vision model | Passed |

Every inference case checks HTTP success and one post-response hook with
provider-reported input/output token counts whose sum matches total usage.
Streaming cases check the public SSE content type; Responses requires a completed
terminal event. This verifies usage propagation, not monetary settlement.

Discovery returned `chat/stream/responses/tools` for MiniCPM and
`chat/stream/responses/vision/tools` for Qwen, with `provider_metadata` provenance.
Both results passed managed-deployment validation. Structured-output support
was checked by inference separately from discovered metadata.

The initial vision fixture lacked `AVEnabled` and was rejected before inference.
With that existing routing prerequisite enabled in the test endpoint, the PNG
request succeeded. Actual security settings were not modified.

## API inventory and Gateway scope

This inventory distinguishes inference compatibility from administration of the
native daemon. A similar URL in Gateway does not imply passthrough semantics.

| Native API group | Gateway status and evidence |
| --- | --- |
| `/v1/chat/completions`, `/v1/completions`, `/v1/responses` | Live coverage above; supported parameter subset |
| Chat targeting an Omni collection | Native orchestration of multiple component models was not tested; compound media usage/billing compatibility is not established |
| `/v1/models`, `/v1/models/{model_id}` | Native list inspected and Gateway discovery tested; public Gateway models are its authorized catalog, not the daemon's raw registry |
| `/v1/embeddings` | Adapter wire/parameter/usage unit tests; no embedding model installed for live inference |
| `/v1/rerank` and native aliases `/v1/reranking`, `/v1/reranker` | Gateway adapter uses `/v1/rerank`; wire/usage unit tests; no reranker installed |
| `/v1/images/generations`, `/v1/images/edits`, `/v1/images/variations` | Adapter wire/media/accounting unit tests; no image-generation model installed |
| `/v1/audio/transcriptions`, `/v1/audio/speech` | Adapter wire/media/accounting unit tests; no dedicated transcription or speech model installed |
| `/realtime` | Native transcription WebSocket is not Gateway's generation Realtime contract; not exposed by this adapter |
| `/v1/images/upscale`, `/v1/classify`, `/v1/audio/generations`, `/v1/3d/generations` | Native specialty operations not implemented by this adapter |
| `/v1/messages` | Gateway bridge tested above; native Anthropic passthrough is not used by the Lemonade adapter |
| `/api/chat`, `/api/generate`, `/api/embed`, `/api/embeddings` | Native Ollama dialect is not used by the Lemonade adapter |
| `/api/tags`, `/api/show`, `/api/ps`, `/api/version` | Native metadata dialect not proxied |
| `/api/pull`, `/api/delete`, `/api/create`, `/api/copy`, `/api/push` | Native administration, not proxied; no management probes performed |
| `/mcp` POST/GET | Native MCP transport not implemented by this provider adapter; Gateway's managed MCP facilities are separate |
| `/v1/tokenize`, `/v1/slots`, slot save/restore/erase actions | Native tokenizer and prompt-cache controls not proxied |
| `/v1/pull`, `/v1/delete`, `/v1/load`, `/v1/unload`, `/v1/models/register` | Native model lifecycle, not proxied or explicitly invoked during validation |
| `/v1/models/check-updates`, model `/files`, model `/options` GET/POST/DELETE | Native inventory/options management, not proxied |
| `/v1/downloads`, `/v1/downloads/control`, `/v1/registry/search`, `/v1/pull/variants` | Native downloads and upstream registry access, not proxied |
| `/v1/routing/validate` | Native routing policy validation, distinct from Gateway routing |
| `/v1/jobs` list/create, job GET/DELETE and pause/interrupt/resume | Native job engine not mapped to Gateway job APIs |
| `/v1/install`, `/v1/install/dry-run`, `/v1/uninstall`, `/v1/cloud/auth` POST/DELETE | Native backend installation and cloud credentials, not proxied or changed |
| `/v1/docs`, `/v1/docs/{page}`, `/v1/health` | Read live for this audit; Gateway health/docs retain their own meaning |
| `/v1/stats`, `/v1/system-stats`, `/v1/system-info`, `/metrics`, `/live`, `/logs/stream` | Native diagnostics not proxied; not all diagnostic endpoints were exercised |
| `/internal/telemetry/flush`, `/internal/aliases` GET/POST/DELETE | Native internal controls not proxied or changed |

Inference automatically loads an installed model when needed. Validation did not
download models, call administrative load/unload endpoints, change saved recipe
options, modify credentials, or change deployments.

## Parameter compatibility and findings

- Chat `repetition_penalty` is translated to native `repeat_penalty`; supported
  range is 1–2. The two Chat output-token aliases cannot be supplied together.
  Sampling probes establish acceptance and successful inference, not proof that
  every backend implements every sampling control identically.
- Responses is stateless: use `store:false`. Stored responses, previous response
  IDs, conversations, background lifecycle, and managed response tools remain
  explicit errors. Native `top_k` and `repeat_penalty` extensions are not public
  Gateway Responses fields.
- Structured output and function calls worked on the installed MiniCPM despite
  the installed Responses documentation listing only a small initial event set.
  Runtime evidence is necessary in addition to endpoint documentation.
- Reasoning content and missing native SSE output indices are handled by the
  previously committed normalization fixes. Both live models passed Responses
  JSON/SSE without the earlier non-message-content error. Generic validation
  still rejects contradictory identities and malformed content or usage.
- A direct native Completions probe with `echo:true` did not echo its prompt.
  Native Completions `logprobs` used a Chat-style `content` object rather than
  legacy token arrays. Gateway's explicit rejection of these parameters remains
  appropriate; converting the shape alone would not establish correct semantics.
- Embeddings require text input, with float/base64 output; token-ID input and
  requested dimensions are rejected. Rerank requires text documents and does not
  silently discard unsupported ranking controls.
- Images require base64 output. Generation permits one image; editing accepts
  one source image. Native multi-image editing and backend-specific generation
  extensions remain outside the implemented public subset.
- Transcription accepts buffered WAV. Speech accepts buffered supported formats;
  raw PCM and native speech streaming are excluded because their format contract
  varies by backend. Live audio/media inference remains unverified.
- Native PDF input is not advertised. Deployment Docling processing is a separate
  Gateway feature; document conversion was not part of this validation.

No additional production-code defect was confirmed in the exercised operations.
The remaining coverage gaps require models for the missing modalities and, for a
full deployed end-to-end accounting check, an authorized test identity and billing
assertions. They must not be reported as passing live compatibility checks.

## Reproduction and checks

From `repos/gateway`, opt in to local inference explicitly:

```sh
LEMONADE_INTEGRATION_BASE_URL=http://localhost:13305 \
LEMONADE_INTEGRATION_MODEL=MiniCPM5-1B-GGUF \
LEMONADE_INTEGRATION_TOOLS=1 \
go test ./internal/gateway ./internal/provider -run '^TestLemonadeLive' -v -count=1
```

For the five Qwen scenarios, set `LEMONADE_INTEGRATION_MODEL=Qwen3.8-27B-GGUF`,
`LEMONADE_INTEGRATION_VISION=1`, and select
`^TestLemonadeLiveGateway/(chat_JSON|chat_SSE|responses_JSON|responses_SSE|chat_vision)$`.
Discovery is a separate `TestLemonadeLiveDiscovery` test. Optional server bearer
authentication comes from `LEMONADE_INTEGRATION_API_KEY`; do not commit its value.
Regular tests skip both live tests when URL/model variables are absent.

Completed successfully with Go 1.25.13 (module declares 1.25.0): `gofmt -w .`,
`go vet ./...`, `go test ./...`, `go test -race ./...`, `go build ./...`.
Environment-gated PostgreSQL integration tests were not enabled in this audit;
the live billing hook does not test database settlement. No container or UI source
changed, so image build, rollout and UI tests were not run for these additions.

Reference pages: [OpenAI wire API](https://lemonade-server.ai/docs/api/openai/),
[llama.cpp extensions](https://lemonade-server.ai/docs/api/llamacpp/),
[native server API](https://lemonade-server.ai/docs/api/lemonade/),
[Ollama dialect](https://lemonade-server.ai/docs/api/ollama/),
[Anthropic dialect](https://lemonade-server.ai/docs/api/anthropic/),
[MCP](https://lemonade-server.ai/docs/api/mcp/).
