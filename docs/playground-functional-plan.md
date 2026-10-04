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
- [x] Added model/agent comparison with shared attachments and independent A2A
  tasks; model tool calls require per-panel approval/decline and explicit typed
  continuation. Pending calls/tasks block new shared prompts; individual panels
  can be cleared without discarding successful results in other panels.
- [x] Corrected native Interactions `steps` history and stateless replay; Messages
  and Interactions require explicit reviewed tool results or decline before a new
  prompt. Added image/PDF input dialects, indexed stream tool assembly, bounded
  result/history validation and retry preservation after continuation failure.
- [x] Extended the dedicated A2A endpoint with bounded conversation history,
  image/PDF parts, completed-task continuity, explicit task refresh/cancel and
  response identity checks. Pending tasks block new prompts, failed refreshes
  preserve known tasks, and stateless responses are visibly independent.
- [x] Added origin/model/identity-bound browser Realtime tickets with encrypted
  credentials, 30-second expiry, single-use consumption and global/owner limits.
  PostgreSQL consumption is atomic across replicas; credential and model grants
  are revalidated before opening the existing Realtime proxy. Browser Realtime
  controls and voice handling are implemented below.
- [x] Added browser Realtime connection/configuration, text turns, explicit
  response cancellation and bounded voice capture/playback. Capture uses a
  same-origin hashed AudioWorklet and PCM16/24 kHz conversion; no microphone
  access occurs before a user action. Credential changes discard late tickets,
  stop tracks and revoke local audio URLs. Partial interrupted output is retained
  with unavailable metrics; provider completion supplies actual token counts.
- [x] Added background Responses pending state, authenticated resource refresh and
  explicit cancellation. Pending work blocks new turns; failed or mismatching
  reads preserve known jobs, late results cannot replace a new conversation, and
  finalized output is committed once with honest status and usage.
- [x] Added background Interactions resource refresh/cancel with native steps,
  checked IDs/statuses, preserved read errors and credential-scope cancellation.
  Terminal failure/cancellation can omit steps without corrupting conversation
  replay; ordinary malformed response content remains rejected.
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


## Browser Realtime authorization

Browser clients obtain a short-lived ticket with authenticated
`POST /v1/realtime/browser-tickets`, supplying a model and same-host browser
origin. They connect to `GET /v1/realtime/browser?model=<model-id>` using the
`ai-gateway.realtime.v1` subprotocol and a second
`ai-gateway.realtime.ticket.<ticket>` subprotocol. The credential and ticket
must never appear in the WebSocket URL or exported examples. Only the stable
subprotocol is echoed to the client; the ticket is not forwarded upstream.

Tickets use the existing `CREDENTIAL_ENCRYPTION_KEY`, separate encryption-key
derivation and authenticated binding to identity, model, origin and expiry.
Storage contains a ticket digest and encrypted credential, with a 30-second
lifetime, at most 1024 pending tickets globally and eight per identity. Memory
storage is process-local; PostgreSQL migration 040 supplies shared atomic
single-use consumption for multiple replicas. Tickets fail closed on expiry,
replay, changed identity, revoked credentials, storage failure or tampering.
The existing bearer-authenticated `/v1/realtime` contract is unchanged.

Browser ticket endpoints return 503 when credential encryption is unavailable.
Opening a connection still requires a routed model/deployment supporting
Realtime; issuing a ticket does not run inference or reserve billing. The
existing proxy enforces policy, rate limits and response billing. The current
browser origin policy allows the same host only; cross-host browser origins
are not implicitly trusted.


The Realtime UI waits for `session.updated` before sending a turn. It supports
explicit current/legacy session dialects, text or voice output, editable model,
voice, instructions and output limit. Voice capture requires a secure browser
context and explicit microphone permission. Recording is limited to 8 MiB;
Stop and send commits the audio buffer and requests a response, while Discard
clears the buffer without inference. Separately billed input transcription is
not enabled. Response cancellation waits for the provider's final status and
usage; closing a connection cannot prove that provider execution was free.

Audio output is decoded as PCM16/24 kHz and made available as a local WAV with
manual playback/download, limited to 8 MiB per response and 16 MiB across
retained turns. Text history is limited to 2 MiB/40 local turns, outgoing socket
backlog to 1 MiB and diagnostics to 50 bounded summaries. Local history eviction
does not delete provider-side conversation items; reconnecting creates a new
session. Unknown token/audio usage remains `—`; finalized cost belongs to
Usage & spend. Console permissions allow microphone only for its own origin
and audio playback only from local blob URLs. Browser connection examples
exclude actual credentials and issued tickets.


Realtime verification uses isolated synthetic HTTP/WebSocket fixtures, without
calling providers or billing. Browser text flow, synthetic voice output with decoded local WAV playback and
real AudioWorklet execution under the console CSP were verified; the latter used an offline generated tone.
Local Chrome fake-device `getUserMedia` remained pending even outside Gateway
code, so physical microphone capture was not exercised. Recorder/worklet
regressions cover resampling, chunk flush, late permission grants, cancellation
and resource cleanup. A real provider/model audio smoke check remains part of
final environment verification.

Browser ticket requests accept an optional `dialect` (`current` or `legacy`).
Omitting it retains legacy behavior for existing clients; Playground explicitly
binds its selected dialect inside the encrypted ticket. Current OpenAI-compatible
connections omit the legacy beta header. Azure deployment URL and API version
remain configured by the administrator and must support the selected dialect.
The native bearer `/v1/realtime` handshake is unchanged.


## Background Responses in Playground

Set `{"background": true}` in Responses advanced JSON and disable streaming.
The selected deployment must support background execution. Queued/in-progress
responses keep their resource ID and block another turn. Refresh reads that ID;
Cancel sends the resource cancellation request without resubmitting generation.
Refresh/cancel errors preserve the known job for another read. Response identity
and status are checked before updating the conversation. A successful cancellation
request can still return an active status; the UI waits for a terminal state.
Pending usage remains unavailable instead of implying finalized zero usage.
Completion latency after manual refresh includes time until the observation and
is labelled accordingly; it is not a provider execution timing measurement.
Clearing, changing model/credential or switching endpoints removes local state
and ignores late results; it does not automatically cancel server execution.

The pending → in-progress → completed UI was verified in an isolated browser
preview using synthetic jobs. No real provider or billing call was made.


Background Interactions use the same explicit non-streaming configuration in
native advanced JSON. Pending jobs preserve the original prompt/tool result and
block another turn. Refresh and cancel use the original interaction resource ID
without replaying generation. A terminal read applies native output steps and
stored continuity once. Final failed/cancelled/incomplete responses may contain
no steps; missing content in ordinary successful replies is still invalid.
Late reads after a credential/model change cannot publish private results.
Pending token usage remains unavailable, and manually observed completion
latency includes the time until refresh. Clearing only removes local state.

The native queued → in-progress → completed flow was verified with synthetic
Interactions in the browser; no real provider or billing call was made.
