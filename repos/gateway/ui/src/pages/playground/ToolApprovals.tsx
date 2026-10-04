import { GatewayButton } from "../../components/GatewayButton";
import { AreaControl } from "./Controls";
import type { ToolInvocation } from "./toolCalls";

type Props = {
  calls: ToolInvocation[]; disabled: boolean;
  onExecute: (index: number) => void; onDecline: (index: number) => void; onContinue: () => void;
  onResultEdit?: (index: number, output: string) => void; onUseResult?: (index: number) => void;
  label?: string; continueLabel?: string;
};

export function ToolApprovals({ calls, disabled, onExecute, onDecline, onContinue, onResultEdit, onUseResult, label = "Tool approvals", continueLabel = "Continue with tool results" }: Props) {
  if (!calls.length) return null;
  return <section className="playground-tool-approvals" aria-label={label}>
    <h3>Review tool calls</h3><p className="muted">Resolve every call, then explicitly continue the conversation.</p>
    {calls.some((call) => !call.nativeApproval && !call.customTool) && <p className="muted">Execution sends the reviewed arguments to the selected MCP server.</p>}
    {calls.some((call) => call.nativeApproval) && <p className="muted">Native MCP approvals are sent to the provider only when you continue. An approval allows the provider to send the reviewed arguments to its configured MCP connection.</p>}
    {calls.some((call) => call.customTool) && <p className="muted">Custom tools require a result you supply or an explicit decline. This browser does not execute their input. Results are sent as text without JSON conversion.</p>}
    {calls.map((call, index) => <article className="playground-turn tool" key={call.id}>
      <strong>{call.nativeApproval ? `${call.nativeApproval.serverLabel}/` : call.serverID ? `${call.serverID}/` : ""}{call.name} · {call.status}</strong><code>{call.id}</code>
      <details open={call.output === undefined}><summary>{call.customTool ? "Tool input" : "Arguments"}</summary><pre>{call.rawArguments.slice(0, 65536)}</pre></details>
      {call.nativeApproval && <p>MCP connection: <code>{call.nativeApproval.serverURL}</code></p>}
      {call.issue && <p role="status" className="form-error">{call.issue}</p>}
      {call.error && <p role="alert" className="form-error">{call.error}{!call.customTool && !call.nativeApproval && " Execution may already have completed; retry uses the same idempotency key."}</p>}
      {call.customTool ? <>
        <AreaControl label={`Custom tool result ${call.id}`} rows={4} value={call.manualOutput ?? ""} disabled={disabled || !!call.issue || !onResultEdit} onUpdate={(value) => onResultEdit?.(index, value)} />
        <div className="playground-actions">
          <GatewayButton disabled={disabled || !!call.issue || !!call.error || !onUseResult} onClick={() => onUseResult?.(index)}>{call.manualOutput ? "Use result for" : "Use empty result for"} {call.name}</GatewayButton>
          <GatewayButton view="outlined" disabled={disabled} onClick={() => onDecline(index)}>Decline {call.name}</GatewayButton>
        </div>
        {call.output !== undefined && <details open><summary>Tool result</summary>{call.output === "" ? <p>Empty tool result</p> : <pre>{call.output}</pre>}</details>}
      </> : call.nativeApproval || call.output === undefined ? <div className="playground-actions">
        <GatewayButton disabled={disabled || !!call.issue} onClick={() => onExecute(index)}>{call.nativeApproval ? "Approve" : call.status === "failed" ? "Retry" : "Execute"} {call.name}</GatewayButton>
        <GatewayButton view="outlined" disabled={disabled} onClick={() => onDecline(index)}>Decline {call.name}</GatewayButton>
      </div> : <details><summary>Tool result</summary><pre>{call.output}</pre></details>}
    </article>)}
    <GatewayButton disabled={disabled || calls.some((call) => call.output === undefined)} onClick={onContinue}>{continueLabel}</GatewayButton>
  </section>;
}
