import { useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { PageTabs } from "../components/PageTabs";
import { StatCard } from "../components/StatCard";
import { formatTimestamp } from "../format";
import { IdentityForm, type DirectoryIdentity, type IdentityKind } from "./directory/IdentityForm";
import { IdentityBudgetsPanel } from "./directory/IdentityBudgetsPanel";
import { IdentityPolicyPanel } from "./directory/IdentityPolicyPanel";
import { IdentityKeysPanel } from "./directory/IdentityKeysPanel";
import { MembershipsPanel } from "./directory/MembershipsPanel";
import { useDirectoryResource } from "./directory/useDirectoryResource";

function suggestedAttachmentID(identity: string) {
  const normalized = identity.trim().replace(/[^A-Za-z0-9_.-]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 112);
  return normalized ? `team-${normalized}` : "";
}

export function IdentityDetailsPage({ kind }: { kind: IdentityKind }) {
  const { id = "" } = useParams();
  const { client, session, hasCapability, restoreSession } = useAuth();
  const navigate = useNavigate();
  const user = kind === "users";
  const admin = hasCapability("admin");
  const path = `/admin/v1/${kind}/${encodeURIComponent(id)}`;
  const data = useDirectoryResource<DirectoryIdentity>(path);
  const subject = data.value;
  const valid = subject?.id === id ? subject : undefined;
  const activeScope = useRef(data.scope); activeScope.current = data.scope;
  const [tab, setTab] = useState("overview");
  const [editor, setEditor] = useState<{ scope: string; value: DirectoryIdentity }>();
  const [notice, setNotice] = useState<{ scope: string; text: string }>();
  const canEdit = (admin || !user) && !valid?.deleted_at;
  async function save(value: DirectoryIdentity) {
    const scope = data.scope;
    const { id: targetID, ...payload } = value;
    await client.request(`/admin/v1/${kind}/${encodeURIComponent(targetID)}`, { method: "PUT", body: payload });
    if (activeScope.current !== scope) return;
    if (user && targetID === session?.user_id) await restoreSession();
    if (activeScope.current !== scope) return;
    setNotice({ scope, text: "Directory settings saved." }); await data.reload();
  }
  if (!valid && data.loading) return <LoadingState />;
  if (!valid && data.error) return <ErrorState message={data.error} retry={() => void data.reload()} />;
  if (!valid) return <ErrorState message={`${user ? "User" : "Team"} was not found or is outside your scope`} retry={() => void data.reload()} />;
  const name = valid.name || valid.email || valid.id;
  const [createdDate, ...createdTime] = formatTimestamp(valid.created_at).split(" ");
  const [updatedDate, ...updatedTime] = formatTimestamp(valid.updated_at).split(" ");
  const policyActions = [
    ...(!user ? [{ label: "Create team attachment", onSelect: () => { const query = new URLSearchParams({ create: "1", attach_team: valid.id }); const suggestion = suggestedAttachmentID(valid.id); if (suggestion) query.set("suggested_id", suggestion); navigate(`/policies?${query}`); } }] : []),
    { label: "Simulate policy impact", onSelect: () => navigate(`/policies?view=simulator&${user ? "user_id" : "team_id"}=${encodeURIComponent(valid.id)}`) }
  ];
  return <><PageHeader eyebrow="Access control" title={name} description={user ? "Directory identity, team memberships and scoped key access." : "Team identity, memberships and scoped key access."} actions={<div className="inline-actions"><Link className="button-link secondary" to={`/${kind}`}>Back to {kind}</Link>{canEdit && <button onClick={() => setEditor({ scope: data.scope, value: valid })}>Edit settings</button>}{admin && <ActionsMenu label={`Policy actions for ${name}`} items={policyActions} />}</div>} />
    {data.error && <ErrorState message={data.error} retry={() => void data.reload()} />}{notice?.scope === data.scope && <p className="operation-result" role="status">{notice.text}</p>}
    <div className="usage-stats-grid"><StatCard label={user ? "Teams" : "Members"} value={user ? (valid.team_ids || []).length : valid.member_count ?? "—"} /><StatCard label="Status" value={valid.deleted_at ? "Deleted" : valid.status} /><StatCard label="Created" value={createdDate} detail={createdTime.join(" ")} /><StatCard label="Updated" value={updatedDate} detail={updatedTime.join(" ")} /></div>
    <PageTabs label={`${user ? "User" : "Team"} workspace`} value={tab} onUpdate={setTab} items={[{ value: "overview", label: "Overview" }, { value: "members", label: user ? "Teams" : "Members" }, { value: "keys", label: "Virtual keys" }, { value: "policy", label: "Models & limits" }, { value: "budgets", label: "Budgets" }, { value: "settings", label: "Settings" }]} />
    {(tab === "overview" || tab === "settings") && <section className="table-card access-group-details"><h2>{user ? "User" : "Team"} details</h2><dl className="detail-grid"><div><dt>{user ? "User" : "Team"} ID</dt><dd>{valid.id}</dd></div><div><dt>External identity</dt><dd>{valid.external_id || "—"}</dd></div>{user ? <><div><dt>Email</dt><dd>{valid.email || "—"}</dd></div><div><dt>Approved global roles</dt><dd>{valid.roles?.join(", ") || "None"}</dd></div></> : <div><dt>Description</dt><dd>{valid.description || "—"}</dd></div>}<div><dt>Your management scope</dt><dd>{admin ? "Global administrator" : "Matching team administrator"}</dd></div></dl>
      {user && <p className="muted">External ID is directory metadata. Sign-in requires an exact, approved identity-provider subject binding; directory roles do not replace membership approvals.</p>}
      {admin && <div className="inline-actions"><button className="secondary" onClick={() => setTab("budgets")}>Manage budgets</button>{user && <Link className="button-link secondary" to="/settings">Manage identity-provider bindings</Link>}</div>}
    </section>}
    {(tab === "overview" || tab === "members") && !valid.deleted_at && <MembershipsPanel key={`${data.scope}:members`} teamID={user ? undefined : id} userID={user ? id : undefined} allowCreate={valid.status === "active"} onChanged={() => void data.reload()} />}
    {(tab === "overview" || tab === "keys") && <IdentityKeysPanel key={`${data.scope}:keys`} kind={user ? "user" : "team"} id={id} />}
    {(tab === "overview" || tab === "policy") && <IdentityPolicyPanel key={`${data.scope}:policy`} kind={user ? "user" : "team"} id={id} readOnly={Boolean(valid.deleted_at)} />}
    {(tab === "overview" || tab === "budgets") && <IdentityBudgetsPanel key={`${data.scope}:budgets`} kind={user ? "user" : "team"} id={id} allowCreate={valid.status === "active" && !valid.deleted_at} />}
    {editor?.scope === data.scope && <IdentityForm kind={kind} initial={editor.value} onClose={() => setEditor((current) => current === editor ? undefined : current)} onSave={save} />}
  </>;
}
