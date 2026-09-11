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
| Chat completions | JSON, tools, structured output, vision input, SSE, generation controls, bounded multi-choice with aggregate reserve; native Cohere v2 text, structured JSON/SSE, generation controls and JSON/SSE function tools/history; native Anthropic web search with citations and actual search usage; domain-allowlisted Anthropic web fetch with citations, content limits and cache exclusion | Additional controls and model-specific policy |
| Responses | Create, indexed SSE assembly, scoped deployment affinity, function tools, MCP passthrough, stateless reasoning history, native input-token count, owned retrieve/delete/cancel/input-items, durable background settlement, generation options and metadata | Remaining provider-specific parameters and counters |
| Response compaction | Native compact contract, bounded opaque output, model authorization and usage settlement | Additional provider-native compact request options as demand is confirmed |
| Embeddings | String/list and bounded token-ID input, exact token-ID accounting, float/base64 output, compatible adapters and native Gemini, Ollama, Cohere v2 and Voyage adapters | Additional provider compatibility |
| Rerank | Query/documents, compatible adapter, native Cohere v2 and Voyage adapters; exact provider token usage | Additional provider-specific request and usage matrices |
| Text completions | Compatible adapters accept string/list and token-ID prompts; Ollama text generation and Mistral FIM accept native string prompts; legacy response normalization, bounded JSON, incremental SSE and buffered fallback | Additional native provider adapters and provider-specific prompt forms |
| Messages | Inbound `/v1/messages` JSON/SSE over the shared Chat pipeline; outbound Anthropic adapter; explicit prompt-cache controls; opaque user metadata; output effort and JSON Schema format; capacity-tier selection and assigned-tier reporting; signed and redacted thinking blocks with history round-trip; thinking-token usage details; native web-search server tool, citations and usage; capability-gated assistant prefill | Additional server tools |
| Anthropic token counting | Native Anthropic, Gemini and Bedrock counters behind `/v1/messages/count_tokens` with authorization, policy, input quotas and bounded transport | Advanced native content blocks and additional provider counters |
| GenerateContent | Native inbound JSON/SSE and context token counting through shared policy; outbound Gemini chat/tools/vision with API-key or GCE/GKE workload authentication | Advanced native options |
| Interactions | Synchronous, incremental SSE and durable background `/v1/interactions` execution plus retrieve, cancel, and delete lifecycle, mapped through Responses authorization, ownership, tool policy, quota, affinity, retry, settlement, and exact billing; string/step input, function tools, structured output, continuity and bounded generation controls; separately attributed usage | Stream resumption and retrieval, agents and provider-native controls |
| Image generation | Authenticated `/v1/images/generations`, explicit capability routing, compatible/Azure and native Gemini transport, bounded URL/base64 results, retries and token-usage settlement | Additional native adapters, streaming and image-specific non-token pricing |
| Image edits | Authenticated `/v1/images/edits`, bounded multipart images/mask, signature validation, DLP/AV projection, explicit capability routing, compatible/Azure and native Gemini transport, bounded inline results and token settlement | Native mask editing, additional provider adapters and image-specific non-token pricing |
| Image variations | Authenticated `/v1/images/variations`, bounded multipart image, signature validation, AV projection, explicit capability routing, compatible/Azure and native Gemini transport, bounded inline results and token settlement | Additional native adapters and image-specific non-token pricing |
| Audio transcription | Authenticated `/v1/audio/transcriptions`, bounded multipart audio, extension/MIME/signature validation, prompt/keyword/speaker-name DLP, AV projection for the source and bounded known-speaker samples, multilingual hints and chunking strategy, explicit capability routing, compatible/Azure transport, native Groq and Gemini transport, and exact token or duration settlement | Streaming, advanced native Gemini transcript structures and additional native provider adapters |
| Audio translation | Authenticated `/v1/audio/translations` through the shared multipart, authorization, content-policy, quota, retry and billing lifecycle; explicit capability isolation; native Gemini with exact token settlement, native Groq plus compatible/Azure transports with preflight container duration and exact duration settlement when token usage is absent | Streaming and additional native provider adapters |
| Text to speech | Authenticated `/v1/audio/speech`, strict bounded text/voice/options, shared text policy, explicit capability routing, compatible/Azure and native Gemini transport, bounded binary audio, retries, token quotas and exact Unicode character settlement | SSE event streaming, additional native provider adapters and duration-priced models |
| Files | Durable PostgreSQL upload/list/metadata/content/delete lifecycle, bounded multipart input, credential-and-user ownership, cursor pagination, RPM admission and atomic per-owner byte quotas | Purpose-specific retention policies and object-storage backends when scale requires them |
| Realtime | Implemented for OpenAI-compatible deployments | Authenticated text-event WebSocket sessions, bounded lifecycle, effective input DLP, per-response quotas and billing, optional buffered output scanning |
| Videos | Compatible-provider create/list/retrieve/delete/content/remix lifecycle, deployment pinning, durable credential-and-user ownership, bounded artifacts, compensation on partial failure and exact duration billing | Native provider adapters, cancellation where the upstream protocol supports it and additional artifact pricing dimensions |
| OCR | Authenticated `/v1/ocr` for bounded HTTPS, inline PDF/image input and owner-scoped durable file references; zero-based page selection, annotation options, DLP/AV projection, native Mistral routing and schema-constrained native Gemini inline PDF/image routing, bounded response validation, retries and exact processed-page settlement | Additional native provider adapters and advanced Gemini extraction controls |
| Moderation | Text batches and text/image input through the shared authentication, policy, routing, retry, observability and billing lifecycle; compatible and native Mistral provider adapters | Additional native provider protocols and credentialed image conformance tests |
| Apply guardrail | Authenticated public execution with attached-policy authorization, access groups, RPM/TPM, fail-closed durable audit, bounded DLP/AV scans and metadata-only monitoring | Additional scanner protocols when justified by configured policy needs |
| Batches | Durable JSONL jobs for Chat, Responses, text completions, embeddings and moderation; owned input/output/error files, bounded asynchronous workers, per-item execution IDs, policy snapshots, quotas, cancellation, expiry and idempotent settlement | Additional batchable endpoint families and provider-native batch transports when required |
| Fine tuning | Compatible-provider create/list/get/cancel/pause/resume/events/checkpoints lifecycle, owned training files and jobs, deployment pinning, persistence compensation, training-token reserve/settlement and owned model deletion | Additional native training providers and asynchronous final-cost reconciliation where providers expose final trained-token usage |
| Assistants | Durable assistant definition CRUD/list and thread create/get/update/delete with credential-and-user ownership, owned file/vector-resource validation, bounded schemas, atomic quotas, cursor-ready storage and optimistic updates | Initial thread messages, message, run, run-step and tool-output execution lifecycle |
| Search | Authenticated `/v1/search` for one or bounded batched queries, domain/country filters, explicit capability routing, compatible transport, bounded result validation, retries and per-query search-unit settlement | Additional native provider adapters, tiered result pricing and managed search-tool registry |
| Skills | Capability-gated native create/list/get/delete and version lifecycle, bounded multipart/binary transport, durable custom-skill ownership and tenant-filtered shared-provider reads | Provider execution references and additional native provider contracts |
| MCP | Registry, connector/tool-specific ACL, policy-filtered discovery, Responses passthrough, bounded Streamable HTTP execution with durable PostgreSQL idempotency, fail-closed audit and exact tool-request settlement | Credentialed MCP servers, additional transports and long-lived session reuse |
| A2A | A2A 1.0 profile-scoped public Agent Cards and authenticated `GetExtendedAgentCard`; text, structured JSON data or bounded inline image `SendMessage` through shared authentication, model policy, quotas, media scans, guardrails, routing and billing; PostgreSQL-backed tasks with credential-and-user ownership, TTL, atomic quota, `GetTask`, filtered/cursor-paginated `ListTasks`, bounded history and continuation with optimistic concurrency; durable asynchronous execution through `returnImmediately`, post-settlement polling reconciliation and cancellation; `SendStreamingMessage` and bounded `SubscribeToTask` with ordered Task, artifact-delta and post-settlement terminal-status SSE events | Push notifications, remote URL inputs and additional media parts |
| Bedrock Invoke | Not implemented | Native protocol, AWS request signing and usage conversion |
| Bedrock Converse | Synchronous `/model/{model}/converse` and streaming `/model/{model}/converse-stream` through shared authorization, tool policy, quota, retry and exact billing; the Bedrock adapter consumes bounded CRC-verified upstream AWS EventStream messages for incremental text, tool arguments and citations, and the native client endpoint emits checksummed AWS EventStream frames while preserving pre-first-event fallback semantics and provider usage settlement; native text/function transport with auto/required/named tool selection, bounded base64 user images and documents with DLP/AV projection and document-aware TPM reserve, native JSON Schema structured output through the shared capability and cache contract, bounded model-specific inference fields with DLP projection and exact-cache identity, provider guardrail selection with explicit trace preservation, output DLP and response-cache bypass, validated document/search/web citations with Unicode-safe Chat annotations and native response conversion, bounded model response field selection using validated JSON Pointers and DLP-scanned native output, bounded request metadata with DLP projection and response-cache bypass, all documented stop reasons with exact native preservation and billable malformed-output results, native default/flex/priority/reserved service tiers and standard/optimized latency selection with fail-closed adapter isolation, strict usage validation, separately attributed requests and AWS workload credentials | Additional native content blocks |
| Containers | Not implemented | Owned lifecycle, expiration and resource accounting |
| Container files | Not implemented | Owner-scoped file operations and storage limits |
| Sandbox | Not implemented | Isolated execution, resource quotas and lifecycle |
| Vector store creation | Durable PostgreSQL create/list/get/update/delete lifecycle with credential-and-user isolation, expiry policy, cursor pagination, RPM admission and atomic per-owner cardinality quotas | File ingestion, storage-byte accounting and search are separate lifecycle increments |
| Vector store files | Durable PostgreSQL attach/list/get/delete lifecycle with credential-and-user isolation, source-file ownership checks, cursor pagination and atomic per-owner quotas | Ingestion processing, indexing failures and storage-byte accounting |
| Vector store search | Not implemented | Retrieval authorization, filters and usage accounting |
| RAG ingestion | Not implemented | Durable document ingestion and index ownership |
| RAG query | Not implemented | Retrieval/generation policy composition and combined accounting |

## Provider and catalog capabilities

| Capability | Current implementation | Remaining work |
| --- | --- | --- |
| Native provider catalog | Anthropic, Ollama, Gemini, Cohere Chat/Rerank/Embeddings, Mistral Chat/Embeddings/FIM, Voyage Embeddings/Rerank and Bedrock Chat; native operations have protocol tests; the admin capability profile reports adapter operations, deployment capabilities, authentication modes and validated core Chat parameter values separately | Additional native providers with protocol tests; extend the request-parameter matrix as controls are added |
| Azure | Native resource-root `/openai/v1` and explicit deployment paths, API version forwarding, API-key and Entra bearer authentication, public/US Government authority and resource audience selection, ambient AKS federation and App Service/Container Apps/VM managed identity with refresh, discovery and shared inference lifecycle | China and other sovereign-cloud contracts after provider availability is confirmed |
| Workload identity | Bedrock SigV4 supports encrypted explicit credentials, environment keys, bounded shared credentials profiles, regional web-identity STS exchange, refreshable ECS/EKS container roles and EC2 IMDSv2 instance roles; Azure Entra supports public/US Government audiences, AKS projected-token federation, local managed-identity endpoints and IMDS; Gemini supports short-lived GCE/GKE metadata tokens | Additional sovereign Azure clouds and federated profile types when justified |
| Model tokenization | Context estimate including tool schemas; native Anthropic, Gemini and Bedrock counter APIs | Exact model tokenizers/counters with versioned provenance |
| Catalog synchronization | Versioned catalog and hot update | Validated upstream sync, rollback and price provenance |
| Arbitrary passthrough | Not implemented | Explicit route allowlists, identity isolation and accounting |
| Parameter policy | Strict unknown-field decoding; native adapter rejection; generation control validation; machine-readable per-adapter accepted values for `reasoning_effort`, `logprobs` and `service_tier`, derived from runtime validators | Model-specific overrides and matrices for controls added to other API families |
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

The major remaining API work includes batches, fine-tuning, realtime sessions,
video jobs and an owned sandbox runtime. Further parameter additions alone cannot
close these families; each needs its execution, authorization and settlement path.
