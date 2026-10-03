import { useEffect, useState } from "react";
import { GatewayButton } from "../../components/GatewayButton";

export const providerName = (provider: string) => ({ entra: "Microsoft Entra", keycloak: "Keycloak", oidc: "Generic OIDC" })[provider] || provider;

export function CopyValue({ label, value }: { label: string; value: string }) {
  const [result, setResult] = useState("");
  useEffect(() => setResult(""), [value]);
  const copy = async () => {
    try { await navigator.clipboard.writeText(value); setResult("Copied"); }
    catch { setResult("Could not copy. Select and copy the value manually."); }
  };
  return <div className="sso-copy-value"><code>{value}</code><GatewayButton view="flat" aria-label={`Copy ${label}`} onClick={() => void copy()}>Copy</GatewayButton>{result && <small role="status">{result}</small>}</div>;
}

export function MappingSummary({ roles, groups, rolesClaim, groupsClaim }: { roles: Record<string, string>; groups?: Record<string, string>; rolesClaim: string; groupsClaim?: string }) {
  const rows = [...Object.entries(roles).map(([source, role]) => ({ kind: "Role", source, role })), ...Object.entries(groups || {}).map(([source, role]) => ({ kind: "Group", source, role }))];
  return <section className="form-card" aria-label="Saved role mappings">
    <h2>Role and group mappings</h2>
    <p>Role claim: <code>{rolesClaim}</code> · Group claim: <code>{groupsClaim || "Not configured"}</code></p>
    <p>Mappings are intersected with approved directory permissions. They do not grant access by themselves.</p>
    {rows.length ? <div className="table-scroll"><table><thead><tr><th>Source</th><th>IdP value</th><th>Gateway role</th></tr></thead><tbody>{rows.map((row) => <tr key={`${row.kind}:${row.source}`}><td>{row.kind}</td><td>{row.source}</td><td>{row.role}</td></tr>)}</tbody></table></div> : <p>No mappings configured.</p>}
  </section>;
}
