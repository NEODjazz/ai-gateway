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
- [ ] Finish media/tool output, policy selection and full conversation verification.
- [ ] Complete endpoint-specific execution and media/tool handling.
- [ ] Complete comparison, compliance and agent views.
- [ ] Run full UI/Go checks, regression tests, browser checks and screenshots.
- [ ] Commit and push tested increments; verify final CI and local deployment.

Completion requires working flows and evidence for every requirement above.
Having a selector, a disabled control or a raw JSON viewer alone is insufficient.
