# API implementation coverage

This inventory tracks independent API families and provider capabilities. A
compatible base URL does not establish native authentication, lifecycle support,
streaming accounting, or full parameter compatibility. An unimplemented family
has no production endpoint; it must not be represented by a placeholder success.

## Delivery rules

Each implementation is a focused change followed by regression tests and its own
commit. Before enabling an endpoint, verify request validation, credential and
model authorization, effective policy, rate limits, accounting, cache isolation,
error propagation and cancellation. Resource IDs require owner isolation, and
asynchronous work requires persistent state and retry-safe settlement. Network
contract tests use local test servers and fake credentials. External provider
availability is not inferred from these tests.

## API families

| Family | Current implementation | Remaining work |
| --- | --- | --- |
| Models | Authenticated list and single-model retrieval filtered by credential, access-group and tag policy; hidden and absent models share the same not-found response | Provider-side deletion is intentionally outside the gateway control plane |
| Chat completions | JSON, tools, structured output, vision input, SSE, generation controls, bounded multi-choice with aggregate reserve; native Cohere v2 text, structured JSON/SSE, generation controls and JSON/SSE function tools/history; native Anthropic web search with citations and actual search usage, domain-allowlisted web fetch with citations, content limits and cache exclusion, and model-specific deprecated top-k sampling; native Together preserves reasoning output/history aliases, exposes exact model-specific reasoning-effort values, normalizes selected-token log probabilities in JSON/SSE and forwards validated min-p, top-k, repetition-penalty and token-bias controls | Additional controls and model-specific policy |
| Responses | Create, indexed SSE assembly, scoped deployment affinity, function tools, MCP passthrough, bounded inline PDF input with explicit deployment capability and scanner projection, stateless reasoning history, native input-token count, owned retrieve/delete/cancel/input-items, durable background settlement, generation options and metadata | Remaining provider-specific parameters and counters |
| Response compaction | Native compact contract, bounded opaque output, model authorization and usage settlement | Additional provider-native compact request options as demand is confirmed |
| Embeddings | String/list and bounded token-ID input, exact token-ID accounting, float/base64 output, compatible adapters and native Gemini, Ollama, Cohere v2 and Voyage adapters | Additional provider compatibility |
| Rerank | Query/text-or-object documents, compatible adapter, native Cohere v2, Voyage, NVIDIA NIM and Together adapters; NVIDIA NIM maps text passages to `/v1/ranking`, supports explicit `NONE`/`END` truncation and requires exact provider token usage; Together accounting also fails closed without exact usage | Additional provider-specific request and usage matrices |
| Text completions | Compatible adapters accept string/list and token-ID prompts; Ollama text generation and Mistral FIM accept native string prompts; legacy response normalization, bounded JSON, incremental SSE and buffered fallback; managed capability profiles expose adapter-specific prompt forms and validated controls | Additional native provider adapters and model-specific controls |
| Messages | Inbound `/v1/messages` JSON/SSE over the shared Chat pipeline; native `/v1/messages/batches` create/list/retrieve/cancel/delete/results lifecycle over durable item jobs; outbound Anthropic adapter; bounded base64, owner-scoped stored and securely fetched HTTPS JPEG/PNG/GIF/WebP images; bounded inline, owner-scoped stored and securely fetched HTTPS PDF/UTF-8 plain-text documents with policy-visible title/context metadata, immutable batch snapshots and per-document native citation control through shared policy, accounting, cache exclusion and capability-isolated routing; block, tool and top-level prompt-cache controls plus capability-isolated zero-output cache-population calls; native global/US inference geography with response verification, cache isolation and priced reserve/settlement; bounded server-side tool-result/thinking context editing with applied-edit reporting; failure-preserving client tool results with adapter and cache isolation; opaque user metadata; output effort, native adaptive/budgeted thinking, model-specific top-k sampling and JSON Schema format; capacity-tier selection and assigned-tier reporting; signed and redacted thinking blocks with history round-trip; thinking-token usage details; versioned native web-search/web-fetch with dynamic filtering, cache and response controls; regex/BM25 tool search with deferred function schemas; versioned managed code-execution blocks; provider-defined memory, bash and text-editor client tools; current computer and browser client toolsets with fixed namespaced actions, per-member configuration and native continuation; strict browser tab/download state validation; owner-checked native skill execution with durable container reuse and deployment affinity, including failure-atomic buffered SSE; capability-gated assistant prefill | Client-controlled stream options, additional server tools and live incremental skill streaming |
| Anthropic token counting | Native Anthropic, Gemini and Bedrock counters behind `/v1/messages/count_tokens` with authorization, policy, input quotas and bounded transport; PDF/plain-text documents, citation controls, top-level prompt-cache TTL, native inference geography, context editing with original/post-edit counts, thinking policy, skills, managed code-execution, client-tool definitions and owner-bound reusable container context are counted on the selected or pinned deployment | Advanced native content blocks and additional provider counters |
| GenerateContent | Native inbound JSON/SSE and context token counting through shared policy; owner-scoped `fileData` for stored images, PDF/plain text, signature-verifiable audio and MP4/WebM video, resolved after authentication through policy, quota, routing and billing; outbound Gemini chat/tools/vision with API-key or GCE/GKE workload authentication; capability-gated per-category native safety thresholds; native Google Search grounding with validated citations, preserved search entry metadata, actual search-request billing and cache bypass; capability-gated native Python code execution with tool ACL, bounded execution history/results, streaming preservation, billing and cache bypass; bounded signature-validated inline WAV, MP3/MPEG, AIFF, AAC, OGG/Opus, FLAC, M4A and WebM audio with AV projection, explicit capability routing and cache bypass | Cached content, additional grounding/server tools, raw audio formats requiring explicit sample metadata and native options |
| Interactions | Synchronous, incremental SSE and durable background `/v1/interactions` execution plus retrieve, cancel, and delete lifecycle, mapped through Responses authorization, ownership, tool policy, quota, affinity, retry, settlement, and exact billing; model and native agent execution; string/step input, function tools, structured output, continuity, bounded generation controls and owner-bound reuse of provider-created agent environments; separately attributed usage; native capability profiles expose validator-accepted input forms, lifecycle controls, generation settings and thinking levels | Inline environment sources, network/secret transforms, stream resumption and additional provider-native controls |
| Image generation | Authenticated `/v1/images/generations`, explicit capability routing, compatible/Azure plus native Gemini, Together and xAI transport, bounded URL/base64 results, retries and token-usage settlement; Together maps bounded `n`, size, response/output format and seed controls, validates ordered output indices and settles exact image units while retaining estimated token provenance; compatible deployments support bounded SSE partial/final images with pre-first-event fallback and exact final-usage settlement; catalog pricing can charge each validated output image and reserves the requested count | Additional native adapters |
| Image edits | Authenticated `/v1/images/edits`, bounded multipart images/mask, signature validation, DLP/AV projection, explicit capability routing, compatible/Azure and native Gemini transport, bounded inline results and token settlement; compatible deployments support bounded SSE partial/final edits with pre-first-event fallback and exact final-usage settlement; catalog pricing can charge each validated output image | Native mask editing and additional provider adapters |
| Image variations | Authenticated `/v1/images/variations`, bounded multipart image, signature validation, AV projection, explicit capability routing, compatible/Azure and native Gemini transport, bounded inline results and token settlement; catalog pricing can charge each validated output image | Additional native adapters |
| Audio transcription | Authenticated `/v1/audio/transcriptions`, bounded multipart AAC/AIFF/FLAC/MP3/MP4/MPEG/MPGA/M4A/OGG/Opus/WAV/WebM audio with extension/MIME/signature validation, prompt/keyword/speaker-name DLP, AV projection for the source and bounded known-speaker samples, multilingual hints and chunking strategy, explicit capability routing, compatible/Azure transport, native Groq, Gemini and Together transport, and exact token or duration settlement; Together accepts only containers with locally verifiable duration and returns bounded JSON with exact duration usage; native Gemini accepts every signature-verifiable format in the public contract and maps word timestamps and speaker diarization into validated words and segments; compatible deployments support bounded SSE deltas, diarized segments and terminal events with lifecycle validation, pre-first-event fallback and exact final usage; complete-output policies receive a terminal-only SSE response; capability profiles expose each adapter's validated controls | Additional native provider adapters |
| Audio translation | Authenticated `/v1/audio/translations` through the shared multipart, authorization, content-policy, quota, retry and billing lifecycle; explicit capability isolation; native Gemini with exact token settlement, native Groq and Together plus compatible/Azure and Mistral transports with preflight container duration and exact duration settlement when token usage is absent; capability profiles expose each adapter's validated controls; the synchronous public contract and all five documented response formats are covered, while unsupported streaming is rejected before provider execution | Additional native provider adapters |
| Text to speech | Authenticated `/v1/audio/speech`, strict bounded text/voice/options, shared text policy, explicit capability routing, compatible/Azure plus native Gemini, Mistral, Groq, Together and xAI transports, bounded binary audio, retries, token quotas and exact Unicode character settlement; compatible deployments support bounded SSE audio deltas with strict terminal usage, pre-first-event fallback and exact token settlement; capability profiles expose validated controls and SSE availability | Additional native provider adapters and duration-priced models |
| Files | Durable PostgreSQL upload/list/metadata/content/delete lifecycle, bounded multipart input, credential-and-user ownership, cursor pagination, RPM admission and atomic per-owner byte quotas | Purpose-specific retention policies and object-storage backends when scale requires them |
| Realtime | Authenticated WebSocket sessions for compatible and native Azure deployments with bounded event/session lifecycle, effective text policy, capability-isolated input/output audio, strict base64 audio events, AV scanning, append/commit/clear accounting, per-response quotas and exact token-detail settlement, cancellation settlement, and optional bounded output scanning; Azure GA and preview URL contracts support API-key, explicit Entra and ambient managed-identity authentication; separately billed input transcription fails closed before provider execution | Independent ASR pricing/admission/settlement, additional native provider protocols, WebRTC and SIP when required |
| Videos | Compatible-provider and native xAI create plus provider-specific list/retrieve/delete/content/remix/extend lifecycle, deployment pinning, durable credential-and-user ownership, bounded artifacts, compensation on partial failure and exact duration billing; capability profiles expose adapter-validated create durations, sizes and reference forms plus native extension durations | Additional native provider adapters, cancellation where the upstream protocol supports it and additional artifact pricing dimensions |
| OCR | Authenticated `/v1/ocr` for bounded HTTPS, inline PDF/image input and owner-scoped durable file references; zero-based page selection, annotation options, DLP/AV projection, native Mistral routing and schema-constrained native Gemini inline PDF/image routing, bounded response validation, retries and exact processed-page settlement; capability profiles expose validated document forms and controls | Additional native provider adapters and advanced Gemini extraction controls |
| Moderation | Text batches and text/image input through the shared authentication, policy, routing, retry, observability and billing lifecycle; compatible and native Mistral provider adapters; capability profiles expose accepted input forms and metadata support | Additional native provider protocols and credentialed image conformance tests |
| Apply guardrail | Authenticated public execution with attached-policy authorization, access groups, RPM/TPM, fail-closed durable audit, bounded DLP/AV scans and metadata-only monitoring | Additional scanner protocols when justified by configured policy needs |
| Batches | Durable JSONL jobs for Chat, native Messages, Responses, response compaction, text completions, embeddings, rerank, search, image generation/edit/variation, audio transcription and translation, text to speech, OCR and moderation; direct native Messages request arrays have owner-scoped pagination, lifecycle control and ordered JSONL results; Messages preserve native request/response envelopes; image and audio inputs use structured inline base64 within the 4 MiB line limit, with streaming rejected; speech output uses an explicit bounded base64 JSON envelope; file and URL references are resolved to immutable owner-scoped content snapshots before queueing; owned input/output/error files, bounded asynchronous workers, per-item execution IDs, policy snapshots, token, duration, character, page and search-unit accounting, quotas, cancellation, expiry and idempotent settlement | Additional batchable endpoint families and provider-native batch transports when required |
| Fine tuning | Compatible-provider create/list/get/cancel/pause/resume/events/checkpoints lifecycle, owned training files and jobs, deployment pinning, persistence compensation, training-token reserve/settlement and owned model deletion; adapter validation and capability profiles cover every create option | Additional native training providers and asynchronous final-cost reconciliation where providers expose final trained-token usage |
| Assistants | Durable assistant definition CRUD/list; thread and message lifecycle; background run create/list/get/cancel and exact-set function tool-output continuation; provider-side code interpreter and file search with owned assistant/thread resources; tool-call transitions and successful message creation are committed atomically with their Run Steps. All resources use owner isolation, quotas, bounded retention and directional pagination | Gateway-hosted code execution and retrieval runtime |
| Search | Authenticated `/v1/search` for one or bounded batched queries, domain/country filters, explicit capability routing, compatible transport, bounded result validation, retries and per-query search-unit settlement; capability profiles expose query forms and all provider controls | Additional native provider adapters, tiered result pricing and managed search-tool registry |
| Skills | Capability-gated native create/list/get/delete and version lifecycle, bounded multipart/binary transport, durable custom-skill ownership and tenant-filtered shared-provider reads; Messages JSON and failure-atomic buffered SSE plus durable Batches execute built-in or owner-checked custom skill references with tool ACL, TPM and billing accounting; provider execution containers have durable owner isolation, expiry and deployment affinity | Additional native provider contracts and live incremental execution where container persistence can remain atomic |
| MCP | Registry, connector/tool-specific ACL, policy-filtered discovery, Responses passthrough, encrypted static server bearer credentials, bounded Streamable HTTP and legacy HTTP+SSE execution, replica-local bounded session reuse, server ping and explicit rejection of unadvertised requests, durable PostgreSQL idempotency, fail-closed audit and exact tool-request settlement | Additional credential schemes and negotiated sampling, roots or elicitation only when a gateway-owned use case is defined |
| A2A | A2A 1.0 profile-scoped public Agent Cards and authenticated `GetExtendedAgentCard`; text, structured JSON data, bounded inline image/audio/MP4/WebM video/PDF/plain-text/Markdown/CSV and securely fetched HTTPS media of the same types through authenticated `SendMessage`; UTF-8 documents enter the shared text-policy and exact token-accounting path, while binary media uses model capability policy and media scans; shared quotas, guardrails, routing and billing; PostgreSQL-backed tasks with credential-and-user ownership, TTL, atomic quota, `GetTask`, filtered/cursor-paginated `ListTasks`, bounded history and continuation with optimistic concurrency; durable asynchronous execution through `returnImmediately`, post-settlement polling reconciliation and cancellation; `SendStreamingMessage` and bounded `SubscribeToTask` with ordered Task, artifact-delta and post-settlement terminal-status SSE events; encrypted owner-scoped push configurations and settlement-aware HTTPS delivery through an atomic PostgreSQL outbox with bounded retries | Additional binary document part types |
| Bedrock Invoke | Synchronous `/model/{model}/invoke` and streaming `/model/{model}/invoke-with-response-stream` for the bounded Anthropic Messages dialect through shared authentication, function-tool policy, quota, retry and exact billing; explicit deployment capabilities, native JSON Schema structured output, explicit prompt-cache checkpoints on text and tools with 5-minute or 1-hour TTL, bearer or AWS SigV4 authentication, strict request/response/event validation, bounded checksummed AWS EventStream transport, cancellation propagation and provider usage conversion | Additional model dialects only when required |
| Bedrock Converse | Synchronous `/model/{model}/converse` and streaming `/model/{model}/converse-stream` through shared authorization, tool policy, quota, retry and exact billing; the Bedrock adapter consumes bounded CRC-verified upstream AWS EventStream messages for incremental text, tool arguments and citations, and the native client endpoint emits checksummed AWS EventStream frames while preserving pre-first-event fallback semantics and provider usage settlement, including separately reported cache-read and cache-write input tokens; native text/function transport with auto/required/named tool selection and explicit prompt-cache checkpoints on system, message and tool boundaries with 5-minute or 1-hour TTL; bounded base64 user images and documents with DLP/AV projection and document-aware TPM reserve, native JSON Schema structured output through the shared capability and cache contract, bounded model-specific inference fields with DLP projection and exact-cache identity, provider guardrail selection with explicit trace preservation, output DLP and response-cache bypass, validated document/search/web citations with Unicode-safe Chat annotations and native response conversion, bounded model response field selection using validated JSON Pointers and DLP-scanned native output, bounded request metadata with DLP projection and response-cache bypass, all documented stop reasons with exact native preservation and billable malformed-output results, native default/flex/priority/reserved service tiers and standard/optimized latency selection with fail-closed adapter isolation, strict usage validation, separately attributed requests and AWS workload credentials | Additional native content blocks |
| Containers | Owner-scoped provider create/list/get/delete lifecycle with exact deployment binding, expiration policy, bounded initial owned-file copies, network deny/allowlist policy, write-only domain secrets, quota admission, billing and compensation on partial failure; capability profiles expose adapter-validated create controls and capability-gated initial files | Additional native container runtimes and provider-reported resource accounting |
| Container files | Owner-checked upload/list/get/content/delete lifecycle through the container's pinned deployment; multipart bytes and owned gateway files are bounded, and pagination is directional | Aggregate per-container file and byte quotas where the provider does not enforce them |
| Sandbox | Authenticated ephemeral code execution through an explicit capability and native isolated-runtime adapter; network is denied by default, input/output and time are bounded, execution uses quota and billing controls, and cleanup runs on every exit path | Additional native runtimes, languages and artifact transfer when justified |
| Vector store creation | Durable PostgreSQL create/list/get/update/delete lifecycle with credential-and-user isolation, expiry policy, cursor pagination, RPM admission and atomic per-owner cardinality quotas | File ingestion and search are separate lifecycle increments |
| Vector store files | Durable PostgreSQL attach/list/get/update-attributes/delete lifecycle with credential-and-user isolation, durable bounded string/number/boolean attributes, source-file ownership checks, bidirectional cursor pagination with timestamp order and status filtering, atomic file-count quotas, overflow-safe per-store byte quotas, bounded parsed-content retrieval, and explicit auto-chunking responses | Static token-based chunking, ingestion processing and indexing failures |
| Vector store file batches | Atomic durable attachment of 1 to 2000 owned files, global or per-file attributes, explicit auto-chunking policy, aggregate count/byte quotas, owner-scoped retrieval, idempotent terminal cancellation and bidirectional file pagination | Background ingestion and partial per-file processing states |
| Vector store search | Owner-scoped synchronous semantic search for stores with up to 20 UTF-8 text/Markdown/CSV/JSON attachments and 1 MiB matched content; bounded exact, equality, membership, numeric-comparison and nested `and`/`or` attribute filters run before content reads; bounded Unicode-safe chunking, model authorization, effective content policy, TPM/budget reserve, one batched embeddings call, exact provider usage settlement and validated cosine ranking | Durable ingestion/indexing and larger stores |
| RAG ingestion | Atomic durable creation or reuse of owner-scoped UTF-8 files and vector stores with attachment quotas, typed attributes and explicit chunking policy | Background parsing/indexing and binary document formats |
| RAG query | Owner-scoped semantic retrieval with optional reranking and Chat generation; each stage independently enforces authorization, content policy, quotas, routing and billing, with JSON and SSE output | Durable precomputed indexes, citation rendering and larger retrieval corpora |

## Provider and catalog capabilities

| Capability | Current implementation | Remaining work |
| --- | --- | --- |
| Native provider catalog | Anthropic, Ollama, Gemini, Cohere Chat/Rerank/Embeddings, Mistral Chat/Embeddings/FIM, Voyage Embeddings/Rerank, Bedrock Chat, Groq, DeepSeek, Cerebras Chat, NVIDIA NIM Chat/Messages/count-tokens/Completions/Responses create/retrieve/cancel/Embeddings/Rerank, Together Chat/Completions/Embeddings/Rerank/Image Generation/Transcription/Translation/Text-to-Speech and xAI Chat/Responses/Embeddings; provider-specific operations have protocol tests; the admin capability profile reports adapter operations, deployment capabilities, authentication modes and validated parameter support separately | Additional provider adapters with protocol tests; extend the request-parameter matrix as controls are added |
| Azure | Native resource-root `/openai/v1` and explicit deployment paths, API version forwarding, API-key and Entra bearer authentication, public/US Government/China authority and resource audience selection, ambient AKS federation and App Service/Container Apps/VM managed identity with refresh, discovery and shared inference lifecycle; Realtime WebSocket uses the native GA model query or preview deployment/API-version query and the same credential chain | Additional sovereign-cloud contracts after provider availability is confirmed |
| Workload identity | Bedrock SigV4 supports encrypted explicit credentials, environment keys, bounded shared credentials profiles, regional web-identity STS exchange, refreshable ECS/EKS container roles and EC2 IMDSv2 instance roles; Azure Entra supports public/US Government/China audiences, AKS projected-token federation, local managed-identity endpoints and IMDS; Gemini supports short-lived GCE/GKE metadata tokens | Additional sovereign Azure clouds and federated profile types when justified |
| Model tokenization | Context estimate including tool schemas; native Anthropic, Gemini, Bedrock and NVIDIA NIM counter APIs | Exact model tokenizers/counters with versioned provenance |
| Catalog synchronization | Versioned catalog and hot update; xAI discovery atomically merges the separately published text and embedding catalogs | Validated upstream sync for additional providers, rollback and price provenance |
| Arbitrary passthrough | Not implemented | Explicit route allowlists, identity isolation and accounting |
| Parameter policy | Strict unknown-field decoding; native adapter rejection; generation control validation; machine-readable per-adapter Chat, Completions, Responses, Interactions, Embeddings, Rerank, Moderation, Search, Image Generation, Image Edit, Image Variation, Audio Transcription, Audio Translation, Text-to-Speech, OCR, Video, Fine-tuning and Container creation support, including accepted values and input/document/prompt/query forms, all derived from runtime validators and locked by profile regressions | Model-specific overrides and equivalent matrices for other API families |
| Provider and deployment quotas | Atomic fixed-window RPM/TPM across inference, token-count, shadow and owned response lifecycle calls; provider totals shared by all linked deployments; bounded memory mode and shared Redis counters; quota-aware fallback | Additional quota dimensions only when backed by an upstream contract |

## Completed increments

- `82b1011`: bounded memory rate-limit identities, overflow-safe memory/Redis
  counters and strict inference decoding. Unit, race and isolated Redis 7 tests.
- `c139a88`: native adapter parameter rejection before provider modules/cache and
  direct execution; terminal errors tested in JSON and streaming paths.
- `2910482`: final SSE usage and cache-token details reach post-response billing;
  parser and router lifecycle regressions.
- `3f66e20`: chat reasoning effort, logprobs/top-logprobs, penalties and logit bias;
  validation, wire round trips, synthetic/native SSE and cache regressions.
- `2eb94e4`: native Anthropic stop sequences and parallel tool controls for Chat
  and Responses, including forced tools, structured output and SSE.
- `006a16a`: bounded upstream choice/tool indices; negative, excessive and valid
  boundary cases tested before forwarding or allocating indexed arrays.
- `22d622a`: legacy Chat function declarations, selection, history and SSE with
  shared tool authorization, token reserve and cache policy.
- `23b3e2c`: native prompt-cache controls, TTL preservation, capability routing,
  token-count context and cache isolation.
- `9d0a27b`: native Cohere rejects `web_fetch_options` before execution instead
  of accepting and dropping the field.
- `b15d413`: complete machine-readable Chat generation-option support profiles
  for every managed provider type, derived from runtime adapter validation.
- `9e3eac9`: Demo Responses explicitly rejects generation, tool and continuity
  parameters that its local implementation cannot honor.
- `dd16398`: machine-readable Responses option, reasoning-effort and service-tier
  profiles for every managed provider type, derived from runtime validation and
  locked by an exact regression matrix.
- `ac6f769`: Demo Embeddings rejects the unsupported `user` option instead of
  silently discarding it.
- `70a73d1`: compatible, OpenRouter, Cohere and Voyage Rerank checks are exposed
  as side-effect-free adapter validators while preserving wire behavior.
- `2e5d04c`: machine-readable Embeddings input/value and Rerank document/control
  profiles derived from adapter validators, with operation/profile consistency
  regressions.
- `d71eade`: legacy Completions is an explicit managed operation with
  validator-derived prompt-form and generation-control profiles for every
  implementing adapter.
- `15e7e0f`: Moderation profiles distinguish compatible multimodal input from
  native Mistral text input and advertise metadata support explicitly.
- `3d6b722`: Search profiles expose scalar/batch queries and every bounded
  provider control through the same request validator used at execution time.
- `45c98a9`: Image Generation profiles expose validated controls for compatible,
  OpenRouter, Gemini and xAI transports through execution-path validators.
- `2a8e50a`: Image Edit profiles expose each adapter's validated controls, mask
  support and maximum accepted image count through execution-path validators.
- `3d0a599`: Image Variation profiles expose the compatible/Azure and Gemini
  controls accepted by their execution-path validators.
- `20af6e7`: Audio Transcription profiles expose validated controls for every
  implementing adapter while duration reserve remains enforced at execution.
- `5f583f6`: Audio Transcription supports bounded compatible SSE with validated
  delta/segment/done ordering, no fallback after emitted data, complete-output
  policy fallback, exact terminal usage settlement and explicit adapter support.
- `88e7732`: Audio Translation profiles expose validated controls for compatible,
  Azure, Mistral, Gemini and Groq execution paths.
- `fd6cf98`: Text-to-Speech profiles expose validated controls for every
  implementing adapter through execution-path validators.
- `6795aff`: Text-to-Speech supports bounded compatible SSE audio events with
  strict base64 and terminal-usage validation, pre-first-event fallback, exact
  token settlement and explicit per-adapter SSE availability.
- `9323119`: OCR profiles distinguish Mistral HTTPS/inline documents and images
  from Gemini inline-only inputs and expose validated extraction controls.
- `73af8a6`: Video creation profiles expose validated duration, size and input
  reference controls for compatible and native xAI execution paths.
- `d0e9f77`: Fine-tuning creation validates all required and optional fields in
  the adapter and publishes the accepted create controls.
- `7b7dfea`: Container creation profiles combine adapter-validated expiration,
  memory and network controls with capability-gated initial file attachment.
- `dd59d54`: Video profiles derive accepted create durations, sizes and image
  reference forms plus native extension durations from execution validators.
- `10ab0de`: Native Interactions profiles expose validator-accepted input forms,
  lifecycle and generation options, and thinking levels for Gemini deployments.
- `94551d0`: Batch jobs accept rerank JSONL items through shared request
  validation, model policy, TPM admission, routing and durable result output.
- `2ab8c57`: Batch jobs accept scalar and multi-query Search items through
  shared validation, policy-visible messages, TPM admission and search-unit billing.
- `59bc7b1`: Batch jobs accept JSON Image Generation items through shared
  validation, model policy, prompt/output token reserve and exact usage settlement.
- `3bfad0f`: Batch jobs accept bounded inline audio transcription and translation
  items through shared model policy, TPM admission, routing and exact token or
  duration settlement; streaming requests are rejected before queueing.
- `4c8938c`: Batch jobs accept bounded inline Image Edit and Image Variation
  items through shared media validation, model policy, TPM admission, routing
  and exact token settlement; streaming edits are rejected before queueing.
- `b0969ce`: Batch jobs accept native Messages JSONL items through the shared
  converter, model/tool policy, schema-aware TPM admission, routing, billing and
  validated native response conversion; streaming is rejected before queueing.
- `a814059`: Image Generation accepts bounded SSE streaming for compatible
  deployments, validates the partial/completed lifecycle and final usage before
  settlement, and prevents fallback after the first emitted event.
- `e6ebb6b`: Image Edit accepts strict multipart streaming controls and bounded
  SSE partial/completed events through the shared content-policy, quota, retry
  and exact usage-settlement lifecycle.
- `5fb2560`: Native Messages batches accept bounded request arrays, validate and
  snapshot every input before atomic persistence, execute through durable jobs,
  and expose owner-isolated list, retrieve, cancel, delete and ordered JSONL
  result operations. Terminal deletion removes item jobs in the same PostgreSQL
  transaction.
- `64e4029`: Native GenerateContent and countTokens resolve owner-scoped
  `fileData` only after authentication, verify the declared and stored MIME type,
  validate image, PDF, text, audio and video content bounds and signatures, and
  then run the resolved input through existing policy, quota, routing and billing.
- `2295897`: Native NVIDIA NIM Rerank maps bounded text passages to
  `/v1/ranking`, exposes `NONE`/`END` truncation, validates the complete ordered
  ranking and requires positive consistent provider token usage before exact
  settlement. Every other rerank adapter rejects the native-only control.
- `4665503`: Image generation, edit and variation reserve catalog cost by the
  requested output count and settle against the validated response count.
  PostgreSQL pins the per-image price for retries and commit, while ClickHouse,
  request logs, usage reports, CSV export, OpenAPI and the admin UI preserve the
  image count as a separate billing dimension.
- `59ced07`: Native Together Image Generation maps `n` up to four results,
  supported dimensions, URL/base64 response format, JPEG/PNG output and seed to
  `/v1/images/generations`. It accepts missing token usage only for this
  explicitly unit-accounted adapter, preserves estimated token provenance, and
  validates bounded output, exact count, ordered indices, model and object.
- `2cef05e`: Native Together Chat preserves `reasoning` in JSON and SSE as the
  public `reasoning_content` field, maps preserved history back to the native
  alias, and accepts only the documented reasoning-effort values for three exact
  upstream model IDs. The capability profile publishes these model-specific
  overrides separately from provider-wide options.
- `07662f1`: Native Together Chat maps public `logprobs=true` to the provider's
  integer control, normalizes selected-token probabilities in JSON and SSE, and
  rejects `top_logprobs` or malformed and incomplete provider probability data.
- `40ebc1b`: Native Together Chat forwards bounded `min_p`, `top_k`,
  `repetition_penalty` and integer `logit_bias` values, rejects malformed values
  before HTTP and advertises the exact controls in its capability profile.

Gateway Go 1.25.13 formatting, vet, full tests and build passed before each new
implementation commit. Full race tests also passed for the generation-control
increment. Deployment evidence is recorded in `production-reliability.md`;
implementation and rollout coverage must be checked separately.

The native Gemini provider now has explicit protocol conversion, reported usage,
parameter rejection and bounded model discovery. Native inbound Messages and
GenerateContent are implemented as listed above. Responses resource lifecycle and
advanced native protocol features remain open. The
remaining lifecycle and media families stay open until their own acceptance
checks pass; this inventory does not declare overall completion.

Gemini protocol regressions use local HTTP servers, including native streaming
through the router into accounting, tool signature round-trips, redirect refusal,
truncated streams and discovery pagination limits. Go vet, tests, race tests and
build passed, as did UI tests, type checking and the production UI build. No live
paid inference or cloud credential validation was performed.

Anthropic model discovery now follows native pagination with bounded time, page
size and record counts. Tests cover sorted/deduplicated results across pages,
later-page failures, cancellation, redirects, pagination limits and credential
isolation through the managed-provider entry point.

## Responses implementation reconciliation (source 04462e5)

The current request type and adapter regressions establish support for `include`,
`store`, `reasoning`, `truncation`, `top_logprobs` and string-valued `metadata` in
native-compatible JSON/SSE execution. Output parsing retains encrypted reasoning,
reasoning summaries, assistant phase, annotations, token probabilities and reasoning
usage details. Indexed snapshots replace stale fields; terminal events end collection.

HTTP and router validation reject invalid generation options and conflicting or
nonpositive output limits. The legacy max_tokens alias becomes max_output_tokens
on native Responses requests. Metadata has entry and Unicode length limits.
Anthropic conversion explicitly rejects unsupported phase and generation metadata.
These checks do not imply support for arbitrary hosted tools or native cloud auth.

Evidence is in `internal/provider/response_*_test.go`,
`internal/gateway/response_stateless_test.go`, and
`internal/openai/response_*_test.go` under `repos/gateway`. The stateless handler
regression executes two turns with auth policy, anonymization and separate usage
lifecycles against a fake upstream. It is not a live-provider integration test.

The recorded local deployment is source 5d10695, Helm revision 118. Later changes
through 04462e5 are tested and committed but are not covered by that rollout.
PostgreSQL integration last passed at source 63dbfa9 using three isolated databases.

The remaining API work is concentrated in additional provider-native contracts,
WebRTC/SIP Realtime transports and larger hosted execution runtimes. Further
parameter additions alone cannot close these areas; each needs its execution,
authorization and settlement path.
