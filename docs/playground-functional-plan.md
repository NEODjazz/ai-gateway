# Playground functional requirements

The Playground uses the existing AI Gateway design system, routing, credential
scope, policy enforcement and billing. UI selections must correspond to actual
requests and responses; unavailable metrics must remain visibly unavailable.
The interface must not weaken mandatory policies or bypass organization scope.

## Conversation and configuration

- Configuration rail and conversation workspace, with responsive layouts.
- Current UI session or an explicit test virtual key; optional gateway base URL.
- Never forward the console credential or browser cookies to another gateway.
- Test keys and conversation content remain in memory and are not persisted by
  the console. Explicit exports include content but exclude credentials.
- Discover authorized models, refresh and allow a manually supplied model ID.
- Temperature, output limit, Top P, streaming, advanced JSON and structured
  output. Invalid controls must fail before transport; unsupported parameters
  must produce a visible error rather than disappear.
- System instructions, multi-turn history, API or browser Responses continuity,
  suggested prompts, clear, cancel, Enter/Shift+Enter and code export in cURL,
  Python and JavaScript.
- Separate text, reasoning and tool output; preserve structured tool calls for
  continuation and expose approval before executing tools.
- Images/PDF attachments, safe media preview/download, copy output, reasoning,
  citations, bounded raw events and request inspection.
- Tags, authorized MCP servers/toolsets, vector stores, guardrails and policy
  selection. Request-level selection must not relax mandatory policy.
- Responses code interpreter and explicit container/file selection.
- Visible input/output/reasoning tokens, time to first token, total latency and
  actual or explicitly estimated cost; never substitute zero for missing usage.

## Endpoint coverage

Expose the existing Gateway contracts for Chat Completions, Responses,
Anthropic Messages, image generation/editing, embeddings, speech synthesis,
audio transcription, A2A messages, MCP calls, Realtime and Interactions.
Each endpoint needs its own request dialect, input validation, streaming or
binary handling, output rendering and regression coverage. Endpoint and model
compatibility must be distinguished from merely having an endpoint selector.

## Compare

- Up to three independently configured models or agents, with shared prompt and
  attachments, independent histories and optional synchronized settings.
- Parallel execution, cancellation, per-panel errors and partial success.
- Side-by-side responses and comparable usage, latency and cost metrics.
- Clear/add/remove panels and export results.

## Compliance

- Quick policy/guardrail test and selected batch tests without model generation.
- Categorized test prompts, search and selection, custom prompt creation,
  expected allow/block outcomes, CSV import/template and result export.
- Bounded concurrency, cancellation and distinct allowed, blocked, failed and
  cancelled results. A scanner outage is never a passed compliance check.
- Test suites exercise policies; they are not regulatory certification.

## Agent builder

- List, create, edit and remove authorized agent configurations using the
  Gateway's existing agent governance contracts.
- Configure name, underlying model, instructions, generation settings and MCP
  tools with enforceable execution bounds.
- Configure, Chat, Batch Test and Connect views; code examples exclude secrets.
- Saved configuration and executable configuration must agree. A metadata-only
  profile must not be presented as a working agent.

## Verification and implementation status

- [x] Checked all four views and the twelve endpoint selections in the supplied
  local demonstration interface.
- [x] Added independent test credential transport and binary JSON responses.
- [x] Added validated text request construction and credential-free code export.
- [x] Implemented the configuration rail and text conversation; verified Enter,
  streaming output, metadata and code dialog in an isolated browser preview.
- [x] Implemented text-model comparison with independent sessions, synchronized
  or individual settings, partial success, cancellation and CSV export.
- [x] Shared bounded text/tool stream handling between conversation and comparison;
  interrupted streams fail and cancelled transports cannot publish late text.
- [x] Added quick and batch policy checks, bounded concurrency, editable test
  suites, search, filters, CSV import/template/export and explicit failure states.
- [x] Added endpoint-specific forms for Messages, Interactions, images, embeddings,
  speech, transcription, A2A and MCP, including native streams and media output.
- [x] Corrected Responses browser history and image input dialects; code export
  includes MCP idempotency and saves speech responses as binary files.
- [x] Added conversation image/PDF uploads, bounded browser history and explicit
  token-cost estimates using user-provided rates; bounded JSON/binary/SSE reads.
- [x] Added shared comparison attachments, refusal rendering, citations, safe image
  output, copy controls and bounded native conversation history; speech download
  names reflect the returned media type.
- [x] Added credential-scoped resource discovery, MCP toolset/schema selection,
  owned Responses vector/container/file selection and optional prompt checks.
  Failed or blocked checks stop generation; code export includes the checks.
- [x] Added explicit approval/decline for selected MCP functions, stable idempotency
  on retries and typed tool-result continuation in Chat and both Responses modes.
  Resource selection survives workspace tabs and resets on credential/model changes.
  Bounded history removes whole conversation groups without orphan tool results.
- [x] Extended saved agent configurations with encrypted instructions and bounded
  generation settings applied by A2A; administrator-only reads, legacy update
  preservation, ciphertext binding and real PostgreSQL restoration have tests.
- [x] Corrected mandatory A2A protocol headers, live public registry refresh and
  versioned configuration compatibility to avoid stale or silently lost settings.
- [x] Added Agent Builder configuration CRUD, saved A2A chat with task continuity
  and task refresh/cancel, explicit bounded batch inference and connection code.
  Unsaved drafts cannot execute; credential changes discard late reads/results.
  The existing one-generation A2A path still has no MCP execution loop.
- [ ] Finish media/tool output, policy selection and full conversation verification.
- [ ] Complete endpoint-specific execution and media/tool handling.
- [ ] Complete comparison, compliance and agent views.
- [ ] Run full UI/Go checks, regression tests, browser checks and screenshots.
- [ ] Commit and push tested increments; verify final CI and local deployment.

Completion requires working flows and evidence for every requirement above.
Having a selector, a disabled control or a raw JSON viewer alone is insufficient.

## Scoped resource discovery

`GET /v1/playground/catalog?model=<model-id>` uses the calling credential or
browser identity, model grants, access groups and credential tags. It returns
only selectable MCP server/toolset names, authorized guardrail policy names,
identity tags and enabled agent names. It excludes credentials, MCP URLs, tool
arguments and agent instructions, and performs no provider or MCP execution.
Catalog lists are limited to 256 entries each, with explicit truncation. Failed
policy discovery is reported independently. Responses use `Cache-Control:
no-store`; callers must reload after changing identity or model.

Guardrail selection and direct application use organization and user bindings
in addition to key, team, model and tag bindings. Selecting a policy does not
change mandatory inference policies or the identity's grants.
