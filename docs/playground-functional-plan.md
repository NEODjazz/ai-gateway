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
  Saved MCP execution and durable approval continuation are described below.
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
- [x] Added explicit saved-agent MCP bindings and synchronous model/tool iterations.
  Every model generation and MCP operation uses existing authentication, policy,
  quota, billing, audit and idempotency paths. Whole-step validation prevents
  invalid batches from partially executing; loop limits and
  current policy revocation are enforced. Required approvals pause before tools.
- [x] Added durable saved-agent task claims before model/tool effects, explicit
  approve/decline in Agent Chat, Compare, A2A and Batch Test, replica-safe CAS,
  stable tool action keys, native history continuation, bounded failure state,
  cancellation intent and fail-closed recovery of interrupted execution.
- [x] Added explicit test-gateway origin trust mirrored in browser CSP and public
  page metadata. Unknown origins fail before transport; session credentials and
  cookies remain excluded from custom calls. Realtime remains same-origin.
- [x] Preserved reviewed JSON numeric literals in direct MCP tool arguments and
  original JSON results in typed model continuation, without browser rounding.
  Object/size validation, explicit approval, cancellation and retry action keys
  remain enforced.
- [x] Added native Responses MCP approval review, explicit typed approve/decline
  continuation, submitted-connection binding and background-response provenance.
  Invalid review batches retain actual output and usage while blocking continuation.
- [x] Added Responses custom tool input review, explicit manual output/decline,
  typed continuation with original call IDs, exact text preservation, bounded
  UTF-8 results and submitted-definition binding in both continuity modes.
- [x] Added explicit manual results for declared Chat/Responses functions without
  MCP bindings, with exact argument/result text, definition binding and typed
  continuation in API/browser/background sessions and each Compare panel.
  Invalid Compare tool batches preserve actual output/usage and require clearing
  the affected panel before a new shared prompt.
- [x] Added canonical file citations, explicit credential-scoped downloads and
  verified raster previews. Generated container files are authorized through an
  owned stored response and its original deployment, without claiming container
  ownership. Current model/tool grants, byte limits, cancellation and stale-read
  rejection have regression coverage. Organization-bound ownership is described
  below.
- [x] Bound native Messages/Interactions tool review to the originally submitted
  definitions, including background completion. Changed definitions fail before
  transport; undeclared calls can only be declined. Invalid batches retain actual
  text and usage and require Clear before another turn.
- [x] Preserved native tool argument numeric literals in JSON/SSE, browser history,
  typed replay and code export, including Gateway Messages and Interactions
  conversion and native Gemini responses. Messages sends its required protocol
  version header in both live requests and exported code.
- [x] Retained identified failed Responses and Interactions output and reported
  usage in JSON, streaming JSON fallback and terminal SSE. Failed executions do
  not advance conversation history or continuity. Direct failures retain reviewed
  tool results; background prompt failures restore their prompt and attachments.
  Repeating execution requires an explicit user action.
- [x] Preserved reviewed tool results and approval decisions across failed
  background continuations in Responses and Interactions. Pending work hides the
  review controls; a failed terminal read restores them for explicit retry using
  the last successful continuity ID. Results, original definitions and numeric
  literals survive both API-managed and browser-managed history.
- [x] Added manual model entry with a populated catalog in Chat, Responses,
  native/media endpoints, Realtime, Compare and Agent Builder. Refresh preserves
  explicit selections; changing the connection discards the previous catalog.
  Manual entry does not grant access or replace a rejected model automatically.
- [x] Added durable saved-agent MCP `SendStreamingMessage`: initial task claim
  before effects, flushed SSE, settled artifacts/status, approval and replay
  continuity, disconnect cleanup and failure-safe settlement. Real HTTP and
  race tests verify flushing/cancellation; PostgreSQL replica coverage runs both
  JSON and SSE. Playground stream controls are implemented below.
- [ ] Finish media/tool output, policy selection and full conversation verification.
- [ ] Complete endpoint-specific execution and media/tool handling.
- [ ] Complete comparison, compliance and agent views.
- [ ] Run full UI/Go checks, regression tests, browser checks and screenshots.
- [ ] Commit and push tested increments; verify final CI and local deployment.

Completion requires working flows and evidence for every requirement above.

## Explicit model selection

Every model picker permits manual entry even when model discovery succeeds.
Refreshing the catalog changes its suggestions without replacing an explicit ID,
discarding conversation history or dropping selected prompt policies. A failed
catalog read retains the selected ID and shows the discovery error. Switching
back to the catalog preserves a listed ID, or explicitly selects its first option
when the current ID is absent. An actual model change resets its conversation and
reviewed results through the existing lifecycle.

Catalog state is bound to the applied connection. Changing credentials or the
gateway URL hides the previous catalog immediately, clears selections and pending
results, and rejects late discovery responses. Manual entry changes no model
grant: normal Gateway authorization, compatibility checks, policy and billing
apply, and a rejected model produces a visible error without substitution.

Regression coverage includes populated/empty/unavailable catalogs, retained
history and policies, credential changes with pending discovery, denied models,
keyboard entry and locked execution controls. Each view's actual request or saved
agent configuration carries the chosen ID. Browser verification under the actual
console CSP used Chat and Responses HTTP handlers and a synthetic HTTP adapter,
then compared a manual ID against an independent catalog selection. Four explicit
requests returned the expected models; code export retained the manual ID and
the browser recorded no errors. No real provider or billing was invoked.
Having a selector, a disabled control or a raw JSON viewer alone is insufficient.

## Failed text execution and reported usage

A successfully transported Responses or Interactions resource can have status
`failed` and still contain provider output and token usage. The console displays
that resource's ID, failed status, partial text, reasoning and reported usage
alongside its error. Explicit zero token counts remain zero; absent usage stays
unavailable. A failed resource is not a successful conversation turn and its ID
must not become the previous response or interaction ID on a subsequent request.

JSON, a JSON response to a streaming request, and recognized terminal SSE events
follow the same rule. Retention requires a valid resource ID; malformed resources,
HTTP failures, standalone error events, failures in nonterminal packets and a
failed final event belonging to a different resource still reject the request.
Existing output bounds, cancellation and credential-change checks remain active.

The console never automatically repeats failed inference. Direct prompt failures
keep the draft and attachments; background prompt failures restore the submitted
draft and attachments when refreshed. Direct tool-continuation failures preserve
reviewed results and the last successful continuity ID for an explicit retry.
Native provider approvals warn that a repeat may execute approved tools again.
The partial failed output can be inspected and copied but is excluded from
subsequent browser conversation history. Clear removes the failed display.

Regression tests cover these transports, usage with explicit zeros, invalid IDs,
HTTP errors, stream identity mismatches, explicit retries, reviewed manual results
and restoration of background prompt attachments. Browser verification used the
real embedded console and Gateway Responses/Interactions handlers with an
isolated synthetic provider: both failed streams displayed output and tokens,
and explicit retries excluded failed resource IDs and output. No real provider
credentials, inference billing or deployment changes were used. Public API
contracts and dependencies are unchanged.

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

Background tool continuations retain the submitted review state separately from
the pending resource. On a failed terminal read, the console restores reviewed
manual function/custom results, native MCP approve/decline decisions, and native
Interactions function results. The failed resource ID and partial output do not
advance successful history. Retrying requires Continue; the console does not
execute tools again automatically. Provider-managed approvals retain their warning
that repeating a continuation may repeat provider-side execution.

Regression tests cover queued failures in both continuity modes, exact JSON
arguments, original definitions, empty/manual outputs and approvals. Retries are
checked against the original submitted body. An isolated browser verified a
Responses tool call → reviewed result → queued continuation → failed refresh →
explicit successful retry under the real console CSP. Its create calls used the
Gateway handler and HTTP provider adapter; resource reads came from a synthetic
lifecycle fixture. The fixture recorded three creates, one resource read, one
explicit retry and no changed continuation. No real provider, credential, billing
or deployment state was involved. Public API contracts remain unchanged.


## Saved-agent MCP execution

Agent configuration accepts up to 32 distinct `mcp_tools` bindings containing
`server_id` and `tool_name`. Omitting the field in an update preserves existing
bindings; an empty array clears them. Agent Builder discovers servers and tools
with the active credential, saves references without schemas/secrets, and shows
saved selections even when discovery fails. Discovery itself uses the existing
resource billing path. Execution discovers authorized schemas afresh; each server
is limited to eight discovery pages and definitions to 2 MiB per run.

Saved agents execute MCP functions through A2A `SendMessage` or
`SendStreamingMessage`. Model/tool iterations remain synchronous. Function
aliases derive from server/tool identity, avoiding collisions across servers;
private bindings cannot be supplied through the public Responses API. Every
iteration goes through the normal inference pipeline with a distinct internal
execution ID. Every tool goes through the MCP runtime's credential/access-group
ACL, server grants, audit, rate limits, billing and idempotency store. Previous
model/tool work remains billable when a later iteration fails.

The saved policy is an upper bound. A current policy can revoke a tool, require
approval or lower maximum calls; expanding a policy does not silently expand a
saved profile. Limits include all calls in a run and all model generations, with
a two-minute deadline per active turn, 2 MiB response capture and a 2 MiB
bound on the complete durable task payload, including native execution history. A tool
step is validated as a whole before executing any call; a step cannot execute if
there is no remaining model iteration for its result. Changed bindings/model/
instructions, disabled agents/policies, malformed arguments, unknown functions,
repeated call IDs and connector failures stop the run.

Agents with MCP bindings require the configured task store, MCP idempotency store
and audit service before discovery or inference. Production uses PostgreSQL. They
return durable tasks rather than independent messages. An initial `messageId`
derives an owner/agent-scoped task ID. Replaying the same ID and content returns
its known task without generation; conflicting content returns 409. This guarantee
lasts for the configured task retention period. New independent messages must use
new IDs. Continuations atomically claim the stored version before effects.

An approval-required step pauses the whole batch in `TASK_STATE_INPUT_REQUIRED`.
`status.message.metadata.ai_gateway_tool_approval` contains `approval_id` and
`calls` with `call_id`, `server_id`, `tool_name` and actual `arguments`. It contains
no connector URLs, credentials, instructions or private execution keys. Continue
with `SendMessage`, matching task/context IDs, a new user message ID and metadata
`ai_gateway_tool_approval: {approval_id, choices: [{call_id, approved}]}`. Supply
one explicit boolean decision for every required call, with no duplicates or
unknown IDs. The challenge is consumed by the atomic transition to WORKING.
Decline supplies a typed error result to the model without invoking the tool.
Newly generated calls need new approval. The original native call/result history
is preserved for subsequent messages in a completed task.

Resume rediscovers tools and rechecks current profile configuration, schema,
connector, credential, access-group, toolset and policy permissions. Captured
allowed grants remain an upper bound. Instructions are reloaded from encrypted
configuration and checked against a digest; they are not copied into task state.
Private state records counters and stable per-run/call action identities. It is
never exposed by public task reads or exports.

Canceling INPUT_REQUIRED stops pending calls without execution. Canceling WORKING
records intent and keeps WORKING until current execution stops and settles; a
successful cancellation request does not claim zero billing. Expired working
claims are marked failed on task read, without restarting unknown effects.
Failures persist terminal status using bounded cancellation-independent cleanup.
Oversized results preserve a bounded terminal task and the last durable intent;
actual MCP execution results remain in the existing call store. A process crash
never automatically retries an interrupted tool.

Task streaming is supported for these MCP agents when the durable task store,
MCP runtime, call store and audit service are available. The Agent Card advertises
that capability only with these prerequisites. Push and `returnImmediately`
remain unsupported; durable background worker execution remains part of the
unfinished endpoint/runtime work. Tool-free agents
retain their existing behavior. This is an intentional response behavior change
for MCP-bound profiles; clients must handle A2A task results and approval states.

Snapshots containing bindings use admin state schema version 3. Older binaries
fail closed rather than silently dropping these settings; clearing bindings allows
version 1/2 snapshots again depending on the remaining configuration. This is an
additive public API field and a configuration compatibility boundary. PostgreSQL
replica-restoration coverage includes encrypted instructions and MCP references.

### Saved-agent task streaming

`SendStreamingMessage` returns ordered A2A SSE events with the same request ID.
The first `task` event is flushed after authentication, model authorization and
the atomic durable execution claim, before model or tool effects. Completion
artifacts and the final status are emitted only after persistence. An approval
pause returns `TASK_STATE_INPUT_REQUIRED` with its existing review challenge;
the decision can continue over JSON or SSE without changing its task/context.
Model responses are buffered per iteration, so this mode does not imply token
deltas, token usage or first-token timing. Streaming controls must report only
what the chosen protocol supplies.

Initial-message and decision replays return the known durable task without new
effects. A replay during an active execution may end with WORKING status; it
does not start a second worker or claim completion. An expired working claim is
reconciled as failed, without automatically repeating uncertain work. Disconnects
cancel request-scoped execution and use bounded cancellation-independent cleanup.
Completed or interrupted effects retain their normal accounting. After the
stream starts, an execution failure publishes its durable failed/canceled status;
a persistence failure instead emits a JSON-RPC SSE error and never a successful
completion. Reads remain necessary when delivery or settlement is uncertain.

Regression tests cover the first flush and durable claim order, real HTTP
disconnect, approval/decline, replay, tool failure, runtime/ACL denial, failed
persistence, expired claims and unchanged per-step billing. The required
PostgreSQL integration test verifies JSON and SSE approval claims across two
store connections, including duplicate decisions, stale CAS and one durable MCP
execution. No new public request fields, configuration format or dependency is
introduced; previously rejected MCP-agent streaming requests now execute.

### Playground agent streaming controls

Agent Chat, Batch Test and the dedicated A2A form expose an explicit task-stream
control; their default remains JSON. Compare applies its existing stream control
to models and agents independently. Connect/code exports use the selected A2A
method and omit the active credential. No new public request fields are added.

The browser checks the RPC, task and context IDs on every update and bounds
transport bytes, event count and artifact count. Text artifact replacements and
append chunks update the same retained assistant turn, without duplicating its
prompt or incrementing discarded-history counts for every event. Invalid events,
foreign identities and data after terminal status fail visibly. A JSON response
to the same streaming request is validated without another inference request.

A premature stream end requires manual Refresh of the known task. A valid WORKING
status remains pending rather than claiming completion. Errors and local Stop
retain the last checked task and partial answer in Chat, Compare, A2A and Batch;
batch updates replace their existing row. Late cancelled events and old credential
results cannot publish. Refresh/Cancel use the ordinary task RPC and never start a
replacement execution automatically. Approval/decline choices stay local until
explicit Continue, which can use SSE with the original task/context/challenge.
Task streaming does not invent token usage, cost or first-token timing.

UI regression tests cover progressive Chat, interrupted recovery without duplicate
history, explicit reviews, batch row retention, mixed model/agent partial success,
endpoint Stop, credential changes and safe code export. Browser verification under
the real embedded console CSP observed a WORKING task before delivery completed,
a retained partial answer after missing final status, manual Refresh, approval
pause/decline/Continue and an independently successful model in Compare. The A2A
form also retained and refreshed an interrupted task. These checks used synthetic
loopback resources; no real provider, external tools, microphone, billing or local
deployment was invoked.

Saved-agent verification covers typed model → MCP result → model continuation,
credential and policy denial, cumulative call/iteration limits, durable approvals,
revoked toolsets, connector changes after discovery, cancellation and private
binding isolation, parallel initial/approval claims, conflicting replays, schema
revocation, cancellation and bounded failure persistence. A native Responses
HTTP adapter fixture verifies actual typed tool-result continuation and large
integer argument preservation. Required PostgreSQL integration coverage checks
replica continuation, owner isolation, CAS and durable call deduplication.
The saved MCP selection was added, removed and saved in an
isolated browser preview with synthetic resources. The durable approval view
was also verified with two pending calls: one explicit approval, one decline,
and a completed task after continuation. No real provider or billing call was
made by this browser verification.

## Custom test Gateway origin verification

Custom test-key connections default to the console's own origin. An administrator
can opt in to at most sixteen exact origins through
`ADMIN_UI_PLAYGROUND_ORIGINS` or Helm `gateway.adminUI.playgroundOrigins`.
HTTPS is required outside loopback. Startup rejects wildcards, credentials,
non-root paths, query/fragment, invalid ports and ambiguous browser IP spellings.
The validated list appears in uncached public page metadata and CSP `connect-src`;
other CSP directives, SSO trust and remote CORS remain unchanged.

Browser verification used the real embedded UI handlers and two isolated loopback
servers. An untrusted port produced a visible error without switching the active
connection. A configured port loaded its models and returned a synthetic Chat
response with the explicit test key; the target rejected any unexpected key or
cookies, and recorded no unsafe request. Remote CORS must also permit the
console origin and the selected endpoint's headers, including `X-Session-ID` for
Chat turns. Realtime continues to reject another origin before ticket transport.
No real provider, billing or deployment call was involved in this verification.

## Direct tool JSON precision

Direct Playground MCP calls send the original reviewed argument JSON after object
and byte-limit validation. Parsing for validation does not replace the wire body.
The bounded original MCP result JSON is retained as the typed tool output rather
than serialized again from JavaScript numbers. Large integer identifiers and
precise decimal literals therefore survive the browser hop. Changed malformed
arguments fail before transport, and invalid/oversized results cannot continue a
model turn. Stable retry keys, explicit execution/decline and normal Gateway
policy, authorization and billing remain unchanged.

Regression coverage reproduces the original rounding of `9007199254740993` to
`9007199254740992`, covers nested integers and precise decimals in actual fetch
bodies/results, and checks bounded response reads, API errors and cancellation.
This scope covers direct tool arguments/results; ordinary resource discovery and
advanced-parameter objects still use the standard browser JSON parser.


## Native Responses MCP approvals

Responses tools configured in advanced JSON can return `mcp_approval_request`.
Playground reviews them alongside selected function calls, with a combined limit
of 32 calls per response. Each native approval shows its original argument JSON,
server label and the server URL from the submitted tool definition. A unique
matching connection and an allowed tool are required; malformed argument objects
or arguments over 64 KiB can only be declined.

Approve and Decline update browser state without calling either inference or the
direct MCP endpoint. Every pending call must be resolved before explicit Continue
sends `mcp_approval_response` with the original approval ID and a boolean decision.
The provider executes native MCP calls. Direct selected functions retain their
separate runtime execution and idempotency behavior.

Continuation uses the saved response ID or retained native browser history,
without adding a user message. The reviewed connection must remain identical;
changing its URL, approval settings, headers or tool grants blocks continuation
until its original definition is restored or the conversation is cleared.
Background resource reads use the originally submitted definitions, even if the
advanced editor changes while the job is pending. Connection/model/endpoint
changes reset the conversation, and existing cancellation checks discard late
results.

Invalid call identities, duplicate IDs, foreign/ambiguous connections and
excessive batches retain the completed provider output and finalized usage, but
block new turns and code export until Clear. Transport failures preserve reviewed
decisions. Native provider continuation has no browser guarantee of exactly-once
execution: failures after sending an approval warn that a manual retry can repeat
execution. No automatic retry occurs. Browser connection binding supplements
existing Gateway credential/tool authorization; the provider validates ownership
and execution of native approval IDs.

Regression coverage includes both continuity modes, background approvals, mixed
native/function batches, explicit decisions, changed connections, invalid
provenance, finalized usage retention and failed continuation. An isolated browser
preview verified one approval and one decline, no execution before Continue,
exactly one continuation and zero direct MCP calls. This uses synthetic responses;
no real provider, remote MCP service or billing was invoked.


## Self-contained console styles

The console retains its existing system-font fallback and no longer imports an
external font stylesheet at runtime. The previous import was blocked by the
existing same-origin stylesheet CSP on every page load. CSP remains unchanged.
The served embedded CSS has a regression test that rejects unresolved runtime
stylesheet imports; local imports must be resolved by the existing UI build.
Browser verification under the current CSP confirmed zero external font requests,
zero external stylesheets and successful Playground rendering.


## Responses custom tool results

Responses tools configured in advanced JSON can return `custom_tool_call`.
Playground displays their plain-text input alongside direct functions and native
MCP approvals, sharing the 32-call review limit. A custom call must match exactly
one submitted custom tool definition. Foreign or malformed calls retain actual
provider output and usage but block further turns until Clear. Inputs over 64 KiB
or explicitly incomplete inputs can only be declined.

The browser does not execute custom tool input. Supply a result, then choose Use
result, or explicitly choose Use empty result or Decline. Results are strings and
are preserved without trimming, JSON parsing or numeric conversion. Editing a
confirmed result invalidates the decision and disables Continue until confirmation.
UTF-8 results exceeding 128 KiB are rejected visibly without truncation or sending
the previous confirmed value. Decisions can be changed before continuation.

Only Continue sends `custom_tool_call_output` with the original `call_id`, using
API continuity or retained browser history without adding a user message. The
submitted custom tool definition must remain unchanged. Background resource reads
use the original submitted definitions; transport failures retain reviewed results
for a manual retry. Credential/model/endpoint changes discard pending decisions
and drafts. Normal Gateway authorization and policy enforcement remain in place.

Regression coverage includes mixed tool batches, ID/definition validation, empty
and arbitrary text results, UTF-8 limits, keyboard behavior, both history modes,
background completion, failed continuation, credential reset and completed-stream
output. A local HTTP adapter test verifies exact custom result strings and usage.
An isolated browser preview under the current CSP verified one manual result and
one decline, zero extra requests before Continue, exactly one continuation and zero
direct MCP calls. No real provider, remote tool service or billing was invoked.


## Manual function results

Chat Completions and Responses functions declared in the submitted `tools` array
can be reviewed without an MCP execution binding. The browser never executes
these functions or evaluates their arguments. A user supplies plain text,
explicitly confirms an empty or nonempty result, or declines the call; editing a
confirmed result makes confirmation necessary again. Every call must be resolved
before the user explicitly continues the conversation.

Manual results are limited to 128 KiB of UTF-8 text. Original argument strings
and supplied result text remain unchanged, including JSON numeric literals and
non-JSON output. Chat uses the original `tool_call_id`; Responses uses typed
`function_call_output` with its original `call_id`, in both API-managed and
browser-managed history. Background responses retain the original submitted
tool definitions. Continuation rejects changed, removed or duplicate definitions
before transport; a failed continuation retains the reviewed results for retry.

A selected MCP function retains its explicit execution path. Ambiguous MCP
bindings do not become manual functions. Unknown or duplicate declared functions,
malformed or oversized argument objects, and incomplete Responses function calls
can only be declined. Manual results never trigger direct MCP transport. Compare
uses separate results, definitions and history per panel; a malformed tool batch
keeps the actual provider response and usage but blocks new shared prompts until
the affected panel is cleared. Other successful panels remain intact.

UI regressions cover definition changes, background provenance, empty results,
UTF-8 limits, retry preservation, keyboard confirmation and panel isolation.
HTTP adapter tests verify all three continuation dialects with empty, plain-text
and declined output. Browser checks under the actual console CSP exercised
Chat, Responses and Compare: no MCP calls, no extra inference before Continue,
no transport for a changed definition, and no console or CSP errors. These checks
used isolated synthetic responses without real provider execution or billing.

## Response file results

The conversation shows at most 32 canonical file citations per output. Source
file downloads use the existing owner-scoped `/v1/files/{id}/content` route;
provider-only source IDs may be unavailable in that store. Generated container
files use the additive
`GET /v1/responses/{id}/containers/{container_id}/files/{file_id}/content` route.
The Gateway checks the authenticated response owner, current model and
code-interpreter grants, immutable deployment binding and the exact citation in
the original provider's retrieved assistant output. It grants no container
ownership or listing access. Reads do not open an inference billing lifecycle.

Generated downloads require a stored response. Selecting API session management
explicitly sends `store: true`; an advanced `store: false` conflicts visibly
before transport. Browser history can still use `store: false` without silently
enabling storage. File errors leave the conversation and actual usage intact;
expired or deleted upstream resources can no longer be read.

Response/Interaction ownership now includes Organization in its versioned
storage key, as well as credential and user. Legacy unscoped ownership records
are deliberately not accepted, including records for identities without an
organization. Existing provider-side Responses/Interactions sessions created
before this change must be restarted; no upstream content is deleted.

File reads are limited to 32 MiB in the Gateway and browser. Unknown-length
upstream bodies are validated before returning a successful file response.
Files download as attachments with no-store/nosniff headers and sanitized local
names; HTML and SVG are never rendered inline. Manual raster previews require
matching PNG/JPEG/GIF/WebP MIME and magic bytes and are limited to 8 MiB, with
one preview per retained response. Download object URLs are released immediately;
preview URLs are released on Close, identity changes, view changes or unmount.
The console CSP permits `blob:` only for local image/audio rendering; scripts,
frames and unapproved connection origins remain restricted.
Pending reads can be cancelled and cannot publish after a scope change even
when a transport ignores cancellation.


## Native tool review and numeric precision

Messages and Interactions tool calls are bound to exactly one tool declaration
in their original submitted request. Background Interactions retain that request's
definitions even if the form changes before refresh. Removing, changing or
ambiguously declaring a reviewed tool blocks continuation and code export before
transport. An undeclared call can only be declined; adding a declaration later
does not authorize it. The browser never executes native functions automatically.

The original JSON text of native `tool_use.input` and `function_call.arguments`
objects is retained privately in memory. Native response reads, indexed stream
assembly, conversation details, replay and cURL/Python/JavaScript exports preserve
large integers and precise decimals. Gateway Messages conversion retains numeric
literals in incoming history, ordinary responses and SSE; Interactions conversion
and the native Gemini JSON/final-stream decoder preserve numeric values too.
Manual tool-result text is forwarded unchanged. Ordinary advanced settings and
resource metadata continue to use the standard JSON parser.

The existing 32-call, 64 KiB argument and 128 KiB result limits remain enforced.
Native JSON reads also have a 2 MiB and 128-level depth bound. Native conversation
retention counts the original argument text within the existing 40-turn/32 MiB
history limit. Invalid tool IDs, duplicate calls or malformed argument batches
retain the completed provider's text and actual usage, while new prompts and code
export stay blocked until Clear. Failed transport preserves reviewed results.

Messages live requests and code examples include `anthropic-version: 2023-06-01`.
The UI API client gains optional internal JSON codec hooks; default callers retain
existing behavior. Some internal native argument maps now use `json.Number`
instead of `float64`. HTTP field names, public request shapes and authentication
remain unchanged; no new dependency is introduced.

Regression coverage includes native definitions, background provenance, failed
continuation, malformed completed streams, actual usage retention, exact fetch
bodies, nested numeric objects, JSON limits and executed JavaScript export.
Browser verification used the real embedded console CSP, native Messages and
Interactions handlers and an isolated synthetic HTTP provider. Both native
continuations retained exact numeric arguments and plain-text results; changing a
definition made no transport call. Export and history rendering were inspected,
and the console recorded no errors. No real provider, remote tool service,
production credentials or billing were used by this verification.
