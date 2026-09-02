import { useState, type FormEvent } from "react";
import { useAuth } from "../auth/AuthContext";
import { ResourcePage } from "../components/ResourcePage";
import { resourceConfigs } from "./resourceConfigs";

function AssociationForm({ kind }: { kind: "team-member" | "organization-team" }) {
  const { client } = useAuth();
  const [parent, setParent] = useState("");
  const [child, setChild] = useState("");
  const [roles, setRoles] = useState("");
  const [status, setStatus] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault(); setStatus("");
    const path = kind === "team-member" ? `/admin/v1/teams/${encodeURIComponent(parent)}/members/${encodeURIComponent(child)}` : `/admin/v1/organizations/${encodeURIComponent(parent)}/teams/${encodeURIComponent(child)}`;
    try { await client.request(path, { method: "PUT", body: kind === "team-member" ? { roles: roles.split(",").map((value) => value.trim()).filter(Boolean) } : {} }); setStatus("Assignment saved"); }
    catch (cause) { setStatus(cause instanceof Error ? cause.message : "Assignment failed"); }
  }
  return <section className="section-block"><h2>{kind === "team-member" ? "Add team member" : "Assign team to organization"}</h2><form className="filter-card" onSubmit={submit}><label>{kind === "team-member" ? "Team ID" : "Organization ID"}<input required value={parent} onChange={(event) => setParent(event.target.value)} /></label><label>{kind === "team-member" ? "User ID" : "Team ID"}<input required value={child} onChange={(event) => setChild(event.target.value)} /></label>{kind === "team-member" && <label>Roles<input value={roles} onChange={(event) => setRoles(event.target.value)} /></label>}<button>Assign</button></form>{status && <div className="operation-result" role="status">{status}</div>}</section>;
}

export function TeamsPage() {
  const { hasCapability } = useAuth();
  return <ResourcePage config={resourceConfigs.teams} allowCreate={hasCapability("admin")} />;
}
export function OrganizationsPage() { return <><ResourcePage config={resourceConfigs.organizations} /><AssociationForm kind="organization-team" /></>; }
