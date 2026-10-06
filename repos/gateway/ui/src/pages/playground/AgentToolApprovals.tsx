import { useEffect, useState } from "react";
import { GatewayButton } from "../../components/GatewayButton";
import { SelectControl } from "./Controls";
import type { AgentTask } from "./agents";

export function AgentToolApprovals({ task, disabled, label = "Agent tool approvals", onContinue }: { task: AgentTask; disabled: boolean; label?: string; onContinue: (choices: { call_id: string; approved: boolean }[]) => void }) {
  const [choices, setChoices] = useState<Record<string, string>>({});
  useEffect(() => setChoices({}), [task.id, task.approval?.id]);
  if (task.state !== "TASK_STATE_INPUT_REQUIRED" || !task.approval) return null;
  const calls = task.approval.calls;
  return <section className="playground-tool-approvals" aria-label={label}>
    <h3>{label}</h3><p className="muted">Review each tool and its actual arguments. Decisions are sent together; no tool runs when you change a selection. Current permissions are checked again by the gateway.</p>
    {calls.map((call, index) => <article className="playground-turn" key={call.id}><strong>{call.server} · {call.tool}</strong><pre>{call.rawArguments ?? JSON.stringify(call.arguments, null, 2)}</pre><SelectControl label={`${label} decision ${index + 1}`} disabled={disabled} value={choices[call.id] || ""} options={[{ value: "", content: "Choose a decision" }, { value: "approve", content: "Approve" }, { value: "decline", content: "Decline" }]} onUpdate={(decision) => setChoices((items) => ({ ...items, [call.id]: decision }))} /></article>)}
    <GatewayButton disabled={disabled || calls.some((call) => !["approve", "decline"].includes(choices[call.id]))} onClick={() => onContinue(calls.map((call) => ({ call_id: call.id, approved: choices[call.id] === "approve" })))}>Continue {label.toLowerCase()}</GatewayButton>
  </section>;
}
