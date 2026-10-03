import { GatewayButton } from "../../components/GatewayButton";
import type { ToolInvocation } from "./toolCalls";

export function ToolApprovals({ calls, disabled, onExecute, onDecline, onContinue, label = "Tool approvals", continueLabel = "Continue with tool results" }: { calls: ToolInvocation[]; disabled: boolean; onExecute: (index: number) => void; onDecline: (index: number) => void; onContinue: () => void; label?: string; continueLabel?: string }) {
  if (!calls.length) return null;
  return <section className="playground-tool-approvals" aria-label={label}><h3>Review tool calls</h3><p className="muted">Execution sends these arguments to the selected MCP server. Approve or decline each call, then explicitly continue the conversation.</p>
    {calls.map((call, index) => <article className="playground-turn tool" key={call.id}><strong>{call.serverID ? `${call.serverID}/` : ""}{call.name} · {call.status}</strong><code>{call.id}</code><details open={call.output === undefined}><summary>Arguments</summary><pre>{call.rawArguments.slice(0, 65536)}</pre></details>
      {call.issue && <p role="status" className="form-error">{call.issue}</p>}{call.error && <p role="alert" className="form-error">{call.error} Execution may already have completed; retry uses the same idempotency key.</p>}
      {call.output === undefined ? <div className="playground-actions"><GatewayButton disabled={disabled || !!call.issue} onClick={() => onExecute(index)}>{call.status === "failed" ? "Retry" : "Execute"} {call.name}</GatewayButton><GatewayButton view="outlined" disabled={disabled} onClick={() => onDecline(index)}>Decline {call.name}</GatewayButton></div> : <details><summary>Tool result</summary><pre>{call.output}</pre></details>}
    </article>)}
    <GatewayButton disabled={disabled || calls.some((call) => call.output === undefined)} onClick={onContinue}>{continueLabel}</GatewayButton>
  </section>;
}
