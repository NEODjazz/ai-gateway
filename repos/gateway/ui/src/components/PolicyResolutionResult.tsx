import { StatCard } from "./StatCard";

export type PolicyResolutionAttachment = {
  id: string;
  policy_name: string;
  scope: string;
  matched_via: string[];
  policy_status: "enabled" | "disabled" | "missing" | "unavailable";
  dlp: boolean;
  output_dlp: boolean;
  av: boolean;
};

export type PolicyResolutionIssue = { attachment_id: string; policy_name: string; code: string };

export type PolicyResolution = {
  matched_attachments: PolicyResolutionAttachment[];
  effective_policies: string[];
  dlp: boolean;
  output_dlp: boolean;
  av: boolean;
  enforceable: boolean;
  issues: PolicyResolutionIssue[];
};

export function PolicyResolutionResult({ result }: { result: PolicyResolution }) {
  return <section className="policy-resolution" aria-label="Policy resolution result">
    <div className="usage-stats-grid">
      <StatCard label="Decision" value={result.enforceable ? "Enforceable" : "Fail closed"} />
      <StatCard label="Matched attachments" value={result.matched_attachments.length.toLocaleString()} />
      <StatCard label="Effective policies" value={result.effective_policies.length.toLocaleString()} />
      <StatCard label="Modules" value={[result.dlp && "Input DLP", result.output_dlp && "Output DLP", result.av && "AV"].filter(Boolean).join(" + ") || "None"} />
    </div>
    <section className="notice-card">
      <h3>Effective policy set</h3>
      <div className="tag-list">{result.effective_policies.map((policy) => <span className="tag" key={policy}>{policy}</span>)}{!result.effective_policies.length && <span className="muted">No enabled policies</span>}</div>
    </section>
    {result.issues.length > 0 && <section className="notice-card policy-resolution-issues">
      <h3>Fail-closed issues</h3>
      {result.issues.map((issue) => <p key={`${issue.attachment_id}-${issue.code}`}><strong>{issue.attachment_id}</strong>: {issue.policy_name} — {issue.code}</p>)}
    </section>}
    <div className="table-card">
      <div className="table-scroll">
        <table>
          <thead><tr><th>Attachment</th><th>Policy</th><th>Matched via</th><th>Policy state</th><th>Modules</th></tr></thead>
          <tbody>{result.matched_attachments.map((attachment) => <tr key={attachment.id}>
            <td>{attachment.id}</td><td>{attachment.policy_name}</td>
            <td><div className="tag-list">{attachment.matched_via.map((item) => <span className="tag" key={item}>{item}</span>)}</div></td>
            <td><span className={`status ${attachment.policy_status === "enabled" ? "enabled" : "error"}`}>{attachment.policy_status}</span></td>
            <td>{[attachment.dlp && "Input DLP", attachment.output_dlp && "Output DLP", attachment.av && "AV"].filter(Boolean).join(" + ") || "—"}</td>
          </tr>)}</tbody>
        </table>
        {!result.matched_attachments.length && <div className="empty-state"><strong>No attachments matched</strong></div>}
      </div>
    </div>
  </section>;
}
