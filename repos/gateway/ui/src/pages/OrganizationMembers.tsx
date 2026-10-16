import { useEffect, useState, type FormEvent } from "react";
import { useAuth } from "../auth/AuthContext";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { GatewayButton } from "../components/GatewayButton";

type Member = { organization_id: string; user_id: string; roles: string[]; status: string };
export function OrganizationMembers({ organization }: { organization: string }) {
  const { client } = useAuth();
  const [page, setPage] = useState<{ data: Member[]; total: number } | null>(null);
  const [offset, setOffset] = useState(0);
  const [reload, setReload] = useState(0);
  const [user, setUser] = useState("");
  const [roles, setRoles] = useState<string[]>(["user"]);
  const [status, setStatus] = useState("active");
  const [error, setError] = useState("");
  const [result, setResult] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    const controller = new AbortController(); setPage(null); setError("");
    void client.request<{ data: Member[]; total: number }>(`/admin/v1/organizations/${encodeURIComponent(organization)}/members?limit=25&offset=${offset}`, { signal: controller.signal }).then((data) => {
      if (controller.signal.aborted) return;
      if (!Array.isArray(data.data) || !Number.isSafeInteger(data.total) || data.total < 0 || data.data.some((member) => member.organization_id !== organization)) throw new Error("Membership inventory does not match this organization.");
      setPage(data);
    }).catch((cause) => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : "Could not load organization membership."); });
    return () => controller.abort();
  }, [client, organization, offset, reload]);
  async function save(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError(""); setResult("");
    try {
      if (!roles.length || !user || user.trim() !== user) throw new Error("Enter an exact existing user ID and select at least one approved role.");
      await client.request(`/admin/v1/organizations/${encodeURIComponent(organization)}/members/${encodeURIComponent(user)}`, { method: "PUT", body: { organization_id: organization, user_id: user, roles, status } });
      setResult("Organization approval saved. Browser sessions recheck membership and roles on each request."); setReload((n) => n + 1);
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save organization approval."); } finally { setBusy(false); }
  }
  return <section className="form-card" aria-label="Organization approvals"><h2>Organization approvals</h2><p>Explicit approval is required for org_admin. OIDC mappings cannot grant membership. A principal binding must also pin the same organization.</p>
    {error && <ErrorState message={error} retry={() => setReload((n) => n + 1)} />}{result && <p role="status">{result}</p>}
    {!page && !error ? <LoadingState /> : page && <><div className="table-scroll"><table><thead><tr><th>User</th><th>Approved roles</th><th>Status</th><th>Approval</th></tr></thead><tbody>{page.data.map((member) => <tr key={member.user_id}><td>{member.user_id}</td><td>{member.roles.join(", ")}</td><td>{member.status}</td><td><GatewayButton disabled={busy} onClick={() => { setUser(member.user_id); setRoles(member.roles); setStatus(member.status); setResult(""); }}>Edit approval for {member.user_id}</GatewayButton></td></tr>)}</tbody></table></div><div className="key-pagination"><span>{page.total ? `${offset + 1}–${offset + page.data.length} of ${page.total}` : "0 results"}</span><div className="page-actions"><GatewayButton disabled={offset === 0} onClick={() => setOffset((n) => Math.max(0, n - 25))}>Previous members</GatewayButton><GatewayButton disabled={offset + 25 >= page.total} onClick={() => setOffset((n) => n + 25)}>Next members</GatewayButton></div></div></>}
    <form onSubmit={save} aria-label="Organization approval"><fieldset className="form-grid" disabled={busy}><label>Organization user ID<input required value={user} onChange={(event) => setUser(event.target.value)} /></label><label>Membership status<select value={status} onChange={(event) => setStatus(event.target.value)}><option value="active">Active</option><option value="disabled">Disabled</option></select></label><div>Approved organization roles{["org_admin", "user", "developer"].map((role) => <label className="checkbox-line" key={role}><input type="checkbox" checked={roles.includes(role)} onChange={(event) => setRoles((current) => event.target.checked ? [...current, role] : current.filter((value) => value !== role))} />{role}</label>)}</div><GatewayButton type="submit">Save organization approval</GatewayButton></fieldset></form>
  </section>;
}
