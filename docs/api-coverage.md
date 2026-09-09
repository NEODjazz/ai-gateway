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
| Chat completions | JSON, tools, structured output, vision input, SSE, generation controls, bounded multi-choice with aggregate reserve; native Cohere v2 text, structured JSON/SSE, generation controls and JSON/SSE function tools/history; native Anthropic web search with citations and actual search usage | Additional controls and model-specific policy |
| Responses | Create, indexed SSE assembly, scoped deployment affinity, function tools, MCP passthrough, stateless reasoning history, native input-token count, owned retrieve/delete/cancel/input-items, generation options and metadata | Durable background lifecycle; remaining provider-specific parameters and counters |
| Response compaction | Native compact contract, bounded opaque output, model authorization and usage settlement | Additional provider-native compact request options as demand is confirmed |
| Embeddings | String/list and bounded token-ID input, exact token-ID accounting, float/base64 output, compatible adapters and native Gemini, Ollama and Cohere v2 adapters | Additional provider compatibility |
| Rerank | Query/documents, compatible adapter, native Cohere v2 adapter and model discovery | Additional provider-specific request and usage matrices |
| Text completions | Compatible adapters accept string/list and token-ID prompts; Ollama text generation and Mistral FIM accept native string prompts; legacy response normalization, bounded JSON, incremental SSE and buffered fallback | Additional native provider adapters and provider-specific prompt forms |
| Messages | Inbound `/v1/messages` JSON/SSE over the shared Chat pipeline; outbound Anthropic adapter; explicit prompt-cache controls; opaque user metadata; output effort and JSON Schema format; thinking-token usage details; native web-search server tool, citations and usage; capability-gated assistant prefill | Thinking content blocks and additional server tools |
| Anthropic token counting | Native Anthropic/Gemini counters behind `/v1/messages/count_tokens` with authorization, policy, input quotas and bounded transport | Advanced native content blocks and additional provider counters |
| GenerateContent | Native inbound JSON/SSE and context token counting through shared policy; outbound Gemini chat/tools/vision | Advanced native options and cloud credentials |
| Interactions | Not implemented | Native lifecycle, resource ownership and accounting |
| Image generation | Authenticated `/v1/images/generations`, explicit capability routing, compatible/Azure transport, bounded URL/base64 results, retries and token-usage settlement | Native provider adapters, streaming and image-specific non-token pricing |
| Image edits | Authenticated `/v1/images/edits`, bounded multipart images/mask, signature validation, DLP/AV projection, explicit capability routing, compatible/Azure transport and token settlement | Provider-specific editing options and image-specific non-token pricing |
| Image variations | Not implemented | Multipart contract and image accounting |
| Audio transcription | Not implemented | Multipart audio validation, duration limits and accounting |
| Text to speech | Not implemented | Binary/stream output and character/audio accounting |
| Realtime | Not implemented | Session authorization, WebSocket lifecycle, quotas and usage settlement |
| Videos | Not implemented | Durable owned jobs, polling/cancellation and artifact accounting |
| OCR | Not implemented | Document validation, native execution and page accounting |
| Moderation | Text batches and text/image input through the shared authentication, policy, routing, retry, observability and billing lifecycle; compatible and native Mistral provider adapters | Additional native provider protocols and credentialed image conformance tests |
| Apply guardrail | Authenticated public execution with attached-policy authorization, access groups, RPM/TPM, fail-closed durable audit, bounded DLP/AV scans and metadata-only monitoring | Additional scanner protocols when justified by configured policy needs |
| Batches | Not implemented | Durable jobs, files/results, quotas and idempotent batch settlement |
| Fine tuning | Not implemented | Owned training jobs, model ownership and training accounting |
| Files | Not implemented | Owner-scoped upload/content/delete, retention and storage quotas |
| Assistants | Not implemented | Assistant/thread/run ownership and lifecycle |
| Search | Not implemented | Execution adapters, authorization and search-unit accounting |
| Skills | Not implemented | Versioned registry, owner isolation and provider execution contract |
| MCP | Registry, ACL and Responses passthrough | Runtime transport, bounded sessions and tool execution accounting |
| A2A | Not implemented | Agent discovery, task ownership and authenticated execution |
| Bedrock Invoke | Not implemented | Native protocol, AWS request signing and usage conversion |
| Bedrock Converse | Not implemented | Native messages, event stream and workload authentication |
| Containers | Not implemented | Owned lifecycle, expiration and resource accounting |
| Container files | Not implemented | Owner-scoped file operations and storage limits |
| Sandbox | Not implemented | Isolated execution, resource quotas and lifecycle |
| Vector store creation | Not implemented | Owned store lifecycle and storage accounting |
| Vector store files | Not implemented | Durable ingestion jobs, file ownership and indexing failures |
| Vector store search | Not implemented | Retrieval authorization, filters and usage accounting |
| RAG ingestion | Not implemented | Durable document ingestion and index ownership |
| RAG query | Not implemented | Retrieval/generation policy composition and combined accounting |

## Provider and catalog capabilities

| Capability | Current implementation | Remaining work |
| --- | --- | --- |
| Native provider catalog | Anthropic, Ollama, Gemini, Cohere Chat/Rerank/Embeddings and Mistral Chat/Embeddings/FIM; compatible HTTP adapter; native operations have protocol tests | Additional native providers with protocol tests |
| Azure | Native resource-root `/openai/v1` and explicit deployment paths, API version forwarding, API-key and static Entra bearer authentication, discovery and shared inference lifecycle | Managed Entra acquisition and refresh |
| Workload identity | Not implemented | AWS signing, GCP credentials, Azure refresh and cancellation |
| Model tokenization | Context estimate including tool schemas; native Anthropic/Gemini counter API | Exact model tokenizers/counters with versioned provenance |
| Catalog synchronization | Versioned catalog and hot update | Validated upstream sync, rollback and price provenance |
| Arbitrary passthrough | Not implemented | Explicit route allowlists, identity isolation and accounting |
| Parameter policy | Strict unknown-field decoding; native adapter rejection; generation control validation | Complete provider/model matrices and policy for new API families |

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

The major remaining API work is background Responses execution, media APIs,
async jobs, resource
storage, search, MCP execution and A2A. Provider workload identity and native cloud
authentication also remain unimplemented. Further parameter additions alone cannot
close these families; each needs its execution, authorization and settlement path.
