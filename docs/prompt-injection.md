# Prompt injection protection

Protection is opt-in in a reusable guardrail policy. Existing policies keep their
behavior when `prompt_injection` is omitted. Saving a policy requires global
`admin`; binding it uses the existing deployment and scoped-attachment controls.
No new provider adapter, service, secret or database migration is required.

## Configure in the UI

Open **AI Gateway → Guardrails → Create/Edit policy → Prompt injection detection**.
Choose `heuristics`, `llm` or `heuristics+llm`. Select an enabled Chat deployment
for the classifier when using LLM detection. Attach the policy directly to a
deployment, or use **Policies** to scope it to identities, models, providers and
deployments. An unattached policy does not protect inference.

**Test Guardrails** uses the same implementation as inference. Tests using LLM
detection make a real classifier call and incur its usage. **Guardrail monitor**
shows `prompt_injection` evaluations and `passed`, `rejected` or `unavailable`,
including detector errors when configured to allow the original request.

| Setting | Default | Behavior |
| --- | --- | --- |
| `heuristics_check` | false in API | Unicode-normalized word windows match a small English/Russian attack catalogue, including small typos and zero-width characters. UI heuristic mode enables this. |
| `llm_api_check` | false | A configured deployment classifies a bounded content projection. |
| `similarity_threshold` | 0.85 | Allowed range 0.75–1; higher values require a closer phrase match. |
| `judge_deployment_id` | none | Required for LLM detection; an enabled Chat deployment, never a request-controlled URL. |
| `judge_system_prompt` | built-in classification instructions | Treat supplied content as untrusted data and return one exact verdict. |
| `safe_response` / `unsafe_response` | `SAFE` / `UNSAFE` | Distinct exact verdicts without whitespace/control characters, up to 64 UTF-8 bytes. Extra explanation, empty output or tool calls are unavailable, not safe. |
| `fail_on_error` | true | Block detector failures. False allows those failures and records unavailable. Detected attacks and classifier accounting failures always block. |
| `skip_unscannable_attachments` | false | Reject binary attachments and unresolved references. True explicitly allows their unscanned contents; readable text remains checked. |
| `timeout_seconds` | 5 | Per-policy timeout, range 1–30 seconds, bounded by caller cancellation. |
| `max_input_bytes` | 262144 | UTF-8 projection limit, range 1024–1048576. Oversize, invalid encoding or excessive nesting is a detector failure. |

At least one detector must be enabled. Setting `prompt_injection` to null/omitting
it removes this check when replacing a policy. For static configuration the same
object is nested in a `GUARDRAIL_POLICIES_JSON` entry. The control plane persists
these settings in its existing versioned JSON snapshot.

## Runtime and scope

For each routed request the detector runs before DLP, AV, anonymization, generation
billing reserve, response cache lookup and the target provider. With both checks
enabled, a heuristic rejection avoids the classifier call. All matching detector
policies must pass; one policy cannot weaken another. Updating detector settings
changes the effective policy fingerprint used by exact/semantic response cache
and provider cached-content bindings.

Inputs include chat messages, tool outputs, function arguments, Responses input
items and instructions, model-visible function definitions/schemas, legacy completion prompts, embeddings, reranking and
moderation text. Textual image-generation prompts, speech input and search queries
also use this check. Native client protocols inherit protection where they use
these normalized routing paths; batch/background execution is checked when the
worker executes inference. Realtime client text/control events are checked before
forwarding, and audio inputs follow the unreadable-attachment policy.

Readable inline `text/*` attachments are decoded with bounds; URLs and file IDs
are never fetched by the detector. Ownership/file resolution must run first.
PDFs converted through Docling become readable text and are checked after
conversion. Native PDFs, images, audio/video and unresolved file references are
unscannable. Pretokenized numeric embedding/completion input is also unscannable
without a tokenizer that can reconstruct its text. Allowing them skips this detector's attachment inspection, not the
existing ownership, AV, DLP or provider-capability checks.

A classifier is an administrator-delegated policy service; its model need not be
in the caller's inference model allowlist. Use a dedicated trusted deployment with
appropriate context capacity, pricing and quotas. Its own DLP/AV/anonymization
checks remain active, but prompt injection checks are excluded to prevent
recursion. It does not retry or fall back to another deployment. The gateway caps
classifier output at 64 tokens and classifier concurrency at 8 active calls plus
32 waiting calls per replica, with a one-second queue deadline. Deployment quotas
and admission also apply.

Each classifier call has a generated execution ID and retains the original
credential, user, team and organization for billing. Completed classification is
charged even when the original request is rejected. Main generation is not charged
when it never ran. Counting and compliance operations can therefore incur
classifier usage without a generation call. Billing reserve/commit failures block
even with `fail_on_error=false`.

## API example

Replace a policy using `PUT /admin/v1/guardrail-policies/protect-input`:

```json
{
  "description": "Check untrusted prompt and document text",
  "dlp": false,
  "av": false,
  "enabled": true,
  "prompt_injection": {
    "heuristics_check": true,
    "llm_api_check": false,
    "fail_on_error": true,
    "skip_unscannable_attachments": false,
    "similarity_threshold": 0.85,
    "timeout_seconds": 5,
    "max_input_bytes": 262144
  }
}
```

`POST /admin/v1/compliance/check` accepts a policy and bounded text projection;
`POST /v1/guardrails/apply` additionally enforces authenticated policy access.
Their `checks.prompt_injection` reports the actual detector outcome. Inference
rejections use the existing content-policy error contract; unavailable fail-closed
checks use the existing guardrail-unavailable contract. Do not rely on heuristic
match fragments: neither responses nor monitor events expose them.

## Limits and privacy

Heuristics recognize an explainable phrase set, not every semantic injection or
language. They can reject quotations of attacks in educational/security documents.
LLM classification depends on the chosen model and can also miss attacks or flag
legitimate instructions. Neither mode replaces authorization, tool ACLs, output
checks or isolation. Calibrate against representative legitimate and adversarial
fixtures before binding a policy to production traffic. Provider-native search,
remote MCP or other tools that execute entirely inside the provider are not
intercepted: this detector cannot inspect hidden tool results fetched during that
provider call. Opaque provider-stored conversation/history is not reconstructed
for this detector; the check covers the projected incoming request. Results reintroduced through Gateway input/tool loops are scanned.

The classifier receives the content projection before the target deployment's
masking. Its own configured transforms apply, and its provider's retention policy
remains relevant. Gateway detector configuration contains no provider credentials;
existing encrypted credentials remain owned by the selected deployment. Guardrail
monitoring records only policy, execution ID, source, outcome and duration. Input
text, matched fragments and raw classifier responses are not retained by that
monitor. The explicit compliance preview retains its existing request-local
anonymized-preview behavior.
