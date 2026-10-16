import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { APIError } from "../../api/client";
import { useAuth } from "../../auth/AuthContext";
import { ChipMultiSelect } from "../../components/ChipMultiSelect";
import { ModalFrame } from "../../components/ModalFrame";
import { directoryRoles } from "./IdentityForm";
import { parseBulkUsers, type BulkUser } from "./bulkUsers";

type Result = { user: BulkUser; status: "ready" | "creating" | "created" | "failed"; message?: string };

export function BulkUsersForm({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const { client } = useAuth();
  const [csv, setCSV] = useState("");
  const [roles, setRoles] = useState(["user"]);
  const [status, setStatus] = useState("active");
  const [results, setResults] = useState<Result[]>();
  const [busy, setBusy] = useState(false);
  const [reading, setReading] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [error, setError] = useState("");
  const active = useRef(true), running = useRef(false), stop = useRef(false), fileGeneration = useRef(0);
  useEffect(() => { active.current = true; return () => { active.current = false; fileGeneration.current++; }; }, []);
  function preview() {
    if (reading) return;
    try { setResults(parseBulkUsers(csv).map((user) => ({ user, status: "ready" }))); setError(""); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not parse CSV."); }
  }
  async function loadFile(file?: File) {
    const generation = ++fileGeneration.current;
    if (!file) { setReading(false); return; }
    if (file.size > 256 * 1024) { setError("CSV exceeds 256 KiB."); setReading(false); return; }
    setReading(true);
    try { const text = await file.text(); if (active.current && generation === fileGeneration.current) { setCSV(text); setError(""); } }
    catch { if (active.current && generation === fileGeneration.current) setError("Could not read CSV file."); }
    finally { if (active.current && generation === fileGeneration.current) setReading(false); }
  }
  async function create(retry = false) {
    if (!results || running.current) return;
    running.current = true; stop.current = false; setStopping(false); setBusy(true); setError("");
    const next = results.map((row) => ({ ...row }));
    let created = 0;
    try {
      for (const row of next) {
        if (!active.current || stop.current) break;
        if (row.status !== (retry ? "failed" : "ready")) continue;
        row.status = "creating"; row.message = undefined; setResults(next.map((item) => ({ ...item })));
        try {
          const saved = await client.request<BulkUser>("/admin/v1/users", { method: "POST", body: { ...row.user, roles, status }, signal: AbortSignal.timeout(15000), maximumResponseBytes: 64 * 1024 });
          if (!saved || saved.id !== row.user.id) throw new Error("Create returned a different identity. Inspect the directory before retrying.");
          row.status = "created"; created++;
        } catch (cause) {
          row.status = "failed";
          row.message = cause instanceof Error ? cause.message : "Create could not be confirmed. Inspect the directory before retrying.";
          if (cause instanceof APIError && [401, 403].includes(cause.status)) { if (active.current) setError("Authorization failed. Remaining users were not attempted."); break; }
        }
        if (active.current) setResults(next.map((item) => ({ ...item })));
      }
    } finally {
      running.current = false;
      if (active.current) { setResults(next); setBusy(false); if (created) onCreated(); }
    }
  }
  const attempted = results?.some((row) => row.status !== "ready");
  const created = results?.filter((row) => row.status === "created").length || 0;
  const failed = results?.filter((row) => row.status === "failed").length || 0;
  const ready = results?.filter((row) => row.status === "ready").length || 0;
  return <ModalFrame label="Bulk create users" onClose={onClose} dismissDisabled={busy}><section className="modal directory-form bulk-users-form">
    <div className="modal-heading"><h2>Bulk create users</h2><button className="icon-button" aria-label="Close bulk user form" disabled={busy} onClick={onClose}>×</button></div>
    <p>Create up to 100 directory users. Each user is a separate audited operation; partial success is possible. This does not send invitations, set passwords or approve identity-provider subjects.</p>
    {!results ? <><label>Upload CSV<input type="file" accept=".csv,text/csv" onChange={(event) => void loadFile(event.target.files?.[0])} /></label><label>CSV users<textarea rows={7} value={csv} onChange={(event) => { fileGeneration.current++; setReading(false); setCSV(event.target.value); }} placeholder={'id,name,email,external_id\nalice,Alice,alice@example.test,'} /></label><p className="muted">Headers: id, name, email, external_id. Each row needs id or email; omitted IDs are generated once at preview. Maximum 256 KiB. CSV stays in this browser until creation.</p>{reading && <p role="status">Reading CSV…</p>}<button disabled={reading} onClick={preview}>Preview users</button></> : <>
      <fieldset disabled={busy || attempted} className="form-grid"><label>Status for new users<select value={status} onChange={(event) => setStatus(event.target.value)}><option value="active">Active</option><option value="disabled">Disabled</option></select></label><div className="span-2"><ChipMultiSelect label="Approved global roles for new users" options={directoryRoles.map((value) => ({ value, label: value }))} value={roles} onChange={setRoles} /></div></fieldset>
      <p>Selected roles apply to every new user. Team and organization roles also require matching memberships. Existing identities are never updated by this form.</p>
      <p role="status">{created} created · {failed} failed · {ready} not attempted{busy ? stopping ? " · Stopping after current user…" : " · Creating…" : ""}</p>
      {busy && <button className="secondary" disabled={stopping} onClick={() => { stop.current = true; setStopping(true); }}>Stop after current user</button>}
      <div className="table-scroll bulk-users-results"><table aria-label="Bulk user outcomes"><thead><tr><th>ID</th><th>Name</th><th>Email</th><th>Result</th></tr></thead><tbody>{results.map((row) => <tr key={row.user.id}><td>{row.status === "created" && !busy ? <Link to={`/users/${encodeURIComponent(row.user.id)}`}>{row.user.id}</Link> : row.user.id}</td><td>{row.user.name || "—"}</td><td>{row.user.email || "—"}</td><td>{row.status === "ready" ? "Not attempted" : row.status === "creating" ? "Creating…" : row.status === "created" ? "Created" : row.message}</td></tr>)}</tbody></table></div>
      {failed > 0 && <p>Inspect conflicts and unconfirmed requests in the directory before retrying. Retrying uses the same IDs and cannot overwrite existing users.</p>}
      <div className="inline-actions">{!attempted && <button className="secondary" disabled={busy} onClick={() => { setResults(undefined); setError(""); }}>Edit CSV</button>}{ready > 0 && <button disabled={busy} onClick={() => void create()}>Create {ready} users</button>}{failed > 0 && <button className="secondary" disabled={busy} onClick={() => void create(true)}>Retry failed users</button>}</div>
    </>}
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="modal-actions"><button className="secondary" disabled={busy} onClick={onClose}>{attempted ? "Done" : "Cancel"}</button></div>
  </section></ModalFrame>;
}
