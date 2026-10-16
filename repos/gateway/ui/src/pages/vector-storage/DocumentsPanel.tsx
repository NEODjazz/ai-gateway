import { useCallback, useState, type FormEvent } from "react";
import { ActionsMenu } from "../../components/ActionsMenu";
import { ErrorState, LoadingState } from "../../components/AsyncState";
import { DataTable } from "../../components/DataTable";
import { ModalFrame } from "../../components/ModalFrame";
import { ChunkingFields } from "./ChunkingFields";
import { UploadDocuments } from "./UploadDocuments";
import { useStorageClient, useStorageQuery, useStorageTask } from "./runtime";
import { bytes, count, object, parseAttributes, parseChunking, parseDocument, parseDocumentPage, resourceID, timestamp, type Chunking, type Document } from "./types";

type OwnedFile = { id: string; filename: string; bytes: number; purpose: string };
type FilesPage = { data: OwnedFile[]; has_more: boolean; last_id?: string };
function parseFiles(value: unknown): FilesPage {
  if (!object(value) || !Array.isArray(value.data) || value.data.length > 100 || typeof value.has_more !== "boolean" || value.data.some((item) => !object(item) || !resourceID(item.id) || typeof item.filename !== "string" || item.purpose !== "assistants" || !count(item.bytes)) || value.has_more && (!value.data.length || value.last_id !== value.data[value.data.length - 1].id)) throw new Error("File storage returned invalid or incomplete metadata.");
  return value as FilesPage;
}

function AttachFileForm({ storeID, onClose, onChanged }: { storeID: string; onClose: () => void; onChanged: () => void }) {
  const client = useStorageClient(), task = useStorageTask();
  const [after, setAfter] = useState(""), [selected, setSelected] = useState<OwnedFile>(), [attributes, setAttributes] = useState("{}"), [chunking, setChunking] = useState<Chunking>({ type: "auto" });
  const files = useStorageQuery(`/v1/files?purpose=assistants&limit=25${after ? `&after=${encodeURIComponent(after)}` : ""}`, parseFiles);
  function submit(event: FormEvent) {
    event.preventDefault();
    void task.run(async (signal, current) => {
      if (!selected) throw new Error("Select a file owned by the active credential.");
      const result = await client.request<{ attachment: unknown }>("/v1/rag/ingest", { method: "POST", signal, body: { file_id: selected.id, vector_store_id: storeID, attributes: parseAttributes(JSON.parse(attributes)), chunking_strategy: parseChunking(chunking) } });
      if (parseDocument(result.attachment, storeID).id !== selected.id) throw new Error("Attachment was not confirmed for the selected file.");
      if (current()) { onChanged(); onClose(); }
    });
  }
  const choices = [...new Map([...(selected ? [selected] : []), ...(files.data?.data || [])].map((file) => [file.id, file])).values()];
  return <ModalFrame label="Attach existing file" onClose={onClose} dismissDisabled={task.busy}><section className="modal directory-form"><h2>Attach existing file</h2>{files.error && <ErrorState message={files.error} retry={() => void files.reload()} />}<p>Only files belonging to the active credential and user are listed. Ingestion validates UTF-8 text, supported formats and size before attaching.</p><form onSubmit={submit}><fieldset className="vector-form-fields" disabled={task.busy}><label>Existing file<select required value={selected?.id || ""} onChange={(event) => setSelected(choices.find((file) => file.id === event.target.value))}><option value="">{files.loading ? "Loading files…" : "Select a file"}</option>{choices.map((file) => <option key={file.id} value={file.id}>{file.filename} · {file.id} · {bytes(file.bytes)}</option>)}</select></label><div className="inline-actions"><button type="button" className="secondary" disabled={files.loading || !after} onClick={() => setAfter("")}>First files</button><button type="button" className="secondary" disabled={files.loading || !files.data?.has_more} onClick={() => setAfter(files.data?.last_id || "")}>Next files</button></div><label>Attributes JSON<textarea rows={3} value={attributes} onChange={(event) => setAttributes(event.target.value)} /></label><ChunkingFields value={chunking} onChange={setChunking} /></fieldset>{task.error && <p className="form-error" role="alert">{task.error}</p>}<div className="modal-actions"><button type="button" className="secondary" disabled={task.busy} onClick={onClose}>Cancel</button><button disabled={task.busy || !selected}>{task.busy ? "Attaching…" : "Attach file"}</button></div></form></section></ModalFrame>;
}

function AttributeForm({ document, onClose, onChanged }: { document: Document; onClose: () => void; onChanged: () => void }) {
  const client = useStorageClient(), task = useStorageTask();
  const [attributes, setAttributes] = useState(JSON.stringify(document.attributes, null, 2));
  return <ModalFrame label="Edit document attributes" onClose={onClose} dismissDisabled={task.busy}><form className="modal compact-modal" onSubmit={(event) => { event.preventDefault(); void task.run(async (signal, current) => {
    const value = parseDocument(await client.request(`/v1/vector_stores/${encodeURIComponent(document.vector_store_id)}/files/${encodeURIComponent(document.id)}`, { method: "POST", signal, body: { attributes: parseAttributes(JSON.parse(attributes)) } }), document.vector_store_id);
    if (value.id !== document.id) throw new Error("The server returned another document.");
    if (current()) { onChanged(); onClose(); }
  }); }}><h2>Edit document attributes</h2><label>Attributes JSON<textarea rows={7} disabled={task.busy} value={attributes} onChange={(event) => setAttributes(event.target.value)} /></label>{task.error && <p role="alert" className="form-error">{task.error}</p>}<div className="modal-actions"><button type="button" className="secondary" disabled={task.busy} onClick={onClose}>Cancel</button><button disabled={task.busy}>{task.busy ? "Saving…" : "Save attributes"}</button></div></form></ModalFrame>;
}

function DocumentPreview({ document, onClose }: { document: Document; onClose: () => void }) {
  const parse = useCallback((value: unknown) => {
    if (!object(value) || value.file_id !== document.id || typeof value.filename !== "string" || !Array.isArray(value.content) || value.content.length > 100 || value.content.some((part) => !object(part) || part.type !== "text" || typeof part.text !== "string")) throw new Error("Document content returned mismatched or invalid data.");
    return value as { filename: string; content: { text: string }[] };
  }, [document.id]);
  const content = useStorageQuery(`/v1/vector_stores/${encodeURIComponent(document.vector_store_id)}/files/${encodeURIComponent(document.id)}/content`, parse);
  return <ModalFrame label="Document content" onClose={onClose}><section className="modal key-form-modal"><div className="modal-heading"><h2>{content.data?.filename || "Document content"}</h2><button className="secondary" onClick={onClose}>Close preview</button></div>{content.error && <ErrorState message={content.error} retry={() => void content.reload()} />}{content.loading && !content.data && <LoadingState />}{content.data?.content.map((part, index) => <details key={index} open={index === 0}><summary>Chunk {index + 1}</summary><pre className="vector-content">{part.text}</pre></details>)}</section></ModalFrame>;
}

export function DocumentsPanel({ storeID, expired, onChanged }: { storeID: string; expired: boolean; onChanged: () => void }) {
  const client = useStorageClient(), task = useStorageTask();
  const [cursors, setCursors] = useState([""]), [status, setStatus] = useState("");
  const [attach, setAttach] = useState(false), [editing, setEditing] = useState<Document>(), [preview, setPreview] = useState<Document>();
  const after = cursors[cursors.length - 1];
  const parse = useCallback((value: unknown) => parseDocumentPage(value, storeID), [storeID]);
  const query = new URLSearchParams({ limit: "25", order: "desc", ...(after ? { after } : {}), ...(status ? { filter: status } : {}) });
  const data = useStorageQuery(`/v1/vector_stores/${encodeURIComponent(storeID)}/files?${query}`, parse);
  function changed() { void data.reload(); onChanged(); }
  function remove(document: Document) {
    if (!window.confirm(`Remove ${document.filename || document.id} from this store? The source file remains available and any other attachments are unchanged.`)) return;
    void task.run(async (signal, current) => {
      const value = await client.request<{ id: string; deleted: boolean }>(`/v1/vector_stores/${encodeURIComponent(storeID)}/files/${encodeURIComponent(document.id)}`, { method: "DELETE", signal });
      if (value?.id !== document.id || !value.deleted) throw new Error("Removal was not confirmed.");
      if (current()) changed();
    });
  }
  return <><section className="section-block"><div className="inline-actions"><h2>Documents</h2><button className="secondary" disabled={data.loading} onClick={() => void data.reload()}>Refresh documents</button>{!expired && <button onClick={() => setAttach(true)}>Attach existing file</button>}</div><label className="vector-status-filter">Status<select value={status} onChange={(event) => { setStatus(event.target.value); setCursors([""]); }}><option value="">All statuses</option>{["completed", "in_progress", "failed", "cancelled"].map((value) => <option key={value}>{value}</option>)}</select></label>
    {data.error && <ErrorState message={data.error} retry={() => void data.reload()} />}{task.error && <ErrorState message={task.error} />}{data.data && (data.loading || data.error || data.previous) && <p role="status">Showing the last successful document page{data.loading ? " while refreshing" : ""}.</p>}
    {!data.data ? data.loading ? <LoadingState /> : null : <><DataTable rows={data.data?.data || []} columns={[{ key: "filename", label: "Document", render: (value, row) => <div>{String(value || row.id)}<small className="directory-summary-detail">{String(row.id)}</small></div> }, { key: "status", label: "Status" }, { key: "usage_bytes", label: "Size", render: (value) => bytes(Number(value)) }, { key: "created_at", label: "Attached", render: (value) => timestamp(Number(value)) }, { key: "chunking_strategy", label: "Chunking", render: (value) => { const c = value as Chunking; return c.type === "auto" ? "Automatic" : `${c.static.max_chunk_size_tokens} / ${c.static.chunk_overlap_tokens} overlap`; } }]} actions={(row) => <ActionsMenu label={`Actions for document ${row.id}`} items={[{ label: "Preview content", disabled: data.loading, onSelect: () => setPreview(row as Document) }, ...(!expired ? [{ label: "Edit attributes", disabled: task.busy || data.loading, onSelect: () => setEditing(row as Document) }, { label: "Remove attachment", disabled: task.busy || data.loading, onSelect: () => remove(row as Document) }] : [])]} />} />
      <div className="inline-actions"><p>Page {cursors.length} · {data.data?.data.length || 0} documents shown{data.data?.has_more ? " · More available" : ""}</p><button className="secondary" disabled={data.loading || cursors.length === 1} onClick={() => setCursors([""])}>First page</button><button className="secondary" disabled={data.loading || cursors.length === 1} onClick={() => setCursors(cursors.slice(0, -1))}>Previous documents</button><button className="secondary" disabled={data.loading || !data.data?.has_more || data.previous || Boolean(data.error)} onClick={() => { const next = data.data?.last_id; if (next && !cursors.includes(next)) setCursors([...cursors, next]); else task.setError("Document pagination did not advance. Return to the first page."); }}>Next documents</button></div></>}
    </section>{!expired && <UploadDocuments storeID={storeID} onChanged={changed} />}
    {attach && <AttachFileForm storeID={storeID} onClose={() => setAttach(false)} onChanged={changed} />}{editing && <AttributeForm document={editing} onClose={() => setEditing(undefined)} onChanged={changed} />}{preview && <DocumentPreview document={preview} onClose={() => setPreview(undefined)} />}
  </>;
}
