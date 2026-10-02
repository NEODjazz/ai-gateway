import { useEffect, useRef, useState } from "react";
import { GatewayButton } from "../../components/GatewayButton";

type Row = { source: string; role: string };
const roles = ["admin", "team_admin", "org_admin", "user", "developer"];
export function parseMappings(value: string, kind: string, tenant: boolean): Record<string, string> {
  let data: unknown;
  try { data = JSON.parse(value); } catch { throw new Error(`${kind} mappings must be a JSON object.`); }
  if (!data || Array.isArray(data) || typeof data !== "object") throw new Error(`${kind} mappings must be a JSON object. Complete each row with a unique IdP value.`);
  const entries = Object.entries(data);
  if (entries.length > (kind === "Group" ? 64 : 128) || entries.some(([source, role]) => source.length > 256 || !source || source.trim() !== source || /[\x00-\x1f\x7f]/.test(source) || typeof role !== "string" || !roles.includes(role) || (tenant && ["admin", "team_admin"].includes(role)))) throw new Error(`${kind} mappings contain an invalid IdP value or Gateway role.`);
  return data as Record<string, string>;
}
function rowsFrom(value: string): Row[] {
  try { const data = JSON.parse(value); return data && !Array.isArray(data) && typeof data === "object" ? Object.entries(data).map(([source, role]) => ({ source, role: String(role) })) : []; } catch { return []; }
}
export function SSOMappings({ kind, value, tenant, onChange }: { kind: "Role" | "Group"; value: string; tenant: boolean; onChange: (value: string) => void }) {
  const [rows, setRows] = useState(() => rowsFrom(value));
  const lastSent = useRef(value);
  useEffect(() => { if (value !== lastSent.current) { setRows(rowsFrom(value)); lastSent.current = value; } }, [value]);
  const allowed = roles.filter((role) => !tenant || !["admin", "team_admin"].includes(role));
  function change(next: Row[]) {
    setRows(next);
    const valid = next.every((row) => row.source && row.role) && new Set(next.map((row) => row.source)).size === next.length;
    const serialized = JSON.stringify(valid ? Object.fromEntries(next.map((row) => [row.source, row.role])) : next, null, 2);
    lastSent.current = serialized; onChange(serialized);
  }
  return <section className="mapping-editor" aria-label={`${kind} mappings`}>
    <h3>{kind} mappings</h3>
    <p>Map exact IdP {kind.toLowerCase()} values to approved Gateway roles. Mappings do not grant directory permissions.</p>
    {rows.map((row, index) => <div className="form-grid" key={index}>
      <label>IdP {kind.toLowerCase()} {index + 1}<input value={row.source} onChange={(event) => change(rows.map((entry, n) => n === index ? { ...entry, source: event.target.value } : entry))} /></label>
      <label>Gateway role for {kind.toLowerCase()} {index + 1}<select value={row.role} onChange={(event) => change(rows.map((entry, n) => n === index ? { ...entry, role: event.target.value } : entry))}><option value="">Select role</option>{!allowed.includes(row.role) && row.role && <option value={row.role}>{row.role} (not allowed)</option>}{allowed.map((role) => <option key={role}>{role}</option>)}</select></label>
      <GatewayButton type="button" onClick={() => change(rows.filter((_, n) => n !== index))}>Remove {kind.toLowerCase()} mapping {index + 1}</GatewayButton>
    </div>)}
    <GatewayButton type="button" onClick={() => change([...rows, { source: "", role: "user" }])}>Add {kind.toLowerCase()} mapping</GatewayButton>
    <details><summary>Advanced {kind.toLowerCase()} mappings</summary><label>{kind} mappings (JSON)<textarea rows={5} value={value} onChange={(event) => onChange(event.target.value)} /></label></details>
  </section>;
}
