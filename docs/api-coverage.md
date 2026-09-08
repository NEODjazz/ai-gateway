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
| Chat completions | JSON, tools, structured output, vision input, SSE, generation controls | Additional controls, model-specific policy and multi-choice accounting |
| Responses | Create, SSE, scoped deployment affinity, function tools, MCP passthrough | More parameters; owned retrieve/delete/cancel/input-items; durable background lifecycle |
| Response compaction | Not implemented | Native compact contract, usage settlement and model authorization |
| Embeddings | String/list input, float output, compatible and native adapters | Provider compatibility matrix, additional input/output encodings |
| Rerank | Query/documents, compatible adapter | Provider-specific request and usage matrix |
| Text completions | Not implemented | Native completion execution and legacy response/SSE contracts |
| Messages | Inbound `/v1/messages` JSON/SSE over the shared Chat pipeline; outbound Anthropic adapter | Thinking, prompt-cache controls, server tools and prefill |
| Anthropic token counting | Native Anthropic/Gemini counters behind `/v1/messages/count_tokens` with authorization, policy, input quotas and bounded transport | Advanced native content blocks and additional provider counters |
| GenerateContent | Outbound native Gemini chat/tools/vision, SSE and usage | Native inbound contract and broader provider options |
| Interactions | Not implemented | Native lifecycle, resource ownership and accounting |
| Image generation | Not implemented | Generation contract and image-specific pricing/usage |
| Image edits | Not implemented | Multipart validation, AV, size limits and accounting |
| Image variations | Not implemented | Multipart contract and image accounting |
| Audio transcription | Not implemented | Multipart audio validation, duration limits and accounting |
| Text to speech | Not implemented | Binary/stream output and character/audio accounting |
| Realtime | Not implemented | Session authorization, WebSocket lifecycle, quotas and usage settlement |
| Videos | Not implemented | Durable owned jobs, polling/cancellation and artifact accounting |
| OCR | Not implemented | Document validation, native execution and page accounting |
| Moderation | Not implemented | Public moderation contract independent of internal DLP/AV |
| Apply guardrail | Internal scans only | Public policy execution contract and audit/authorization |
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
| Native provider catalog | Anthropic, Ollama, Gemini; compatible HTTP adapter | Additional native providers with protocol tests |
| Azure | Compatible HTTP scenarios only | Native endpoint/version behavior, Entra identity and refresh |
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

Gateway Go 1.25.13 formatting, vet, full tests and build passed before each new
implementation commit. Full race tests also passed for the generation-control
increment. These commits have not been rolled out to the local deployment.

The native Gemini provider now has explicit protocol conversion, reported usage,
parameter rejection and bounded model discovery. Native inbound protocols and
Responses lifecycle remain next implementation areas. The
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
