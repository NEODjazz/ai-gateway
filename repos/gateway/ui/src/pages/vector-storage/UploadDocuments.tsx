import { useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { ChunkingFields } from "./ChunkingFields";
import { parseAttributes, parseChunking, parseDocument, parseVectorStore, resourceID, type Chunking } from "./types";
import { useStorageClient, useStorageTask } from "./runtime";
import { encodeDocument, uploadAccept, validateUploads } from "./uploads";

type Outcome = { file: File; status: "pending" | "uploading" | "attached" | "failed"; error?: string; fileID?: string };
export function UploadDocuments({ storeID, onChanged }: { storeID: string; onChanged: () => void }) {
  const client = useStorageClient(), task = useStorageTask();
  const [rows, setRows] = useState<Outcome[]>([]), [attributes, setAttributes] = useState("{}"), [chunking, setChunking] = useState<Chunking>({ type: "auto" });
  const stop = useRef(false);
  function select(files: File[]) {
    try { validateUploads(files); setRows(files.map((file) => ({ file, status: "pending" }))); task.setError(""); }
    catch (cause) { task.setError(cause instanceof Error ? cause.message : "Invalid files."); }
  }
  function submit(event: FormEvent) {
    event.preventDefault();
    void task.run(async (signal, current) => {
      const attrs = parseAttributes(JSON.parse(attributes)); const strategy = parseChunking(chunking);
      validateUploads(rows.map((row) => row.file)); stop.current = false;
      const queue = rows.map((row) => ({ ...row }));
      for (let index = 0; index < queue.length; index++) {
        if (!current() || stop.current) break;
        if (queue[index].status === "attached") continue;
        queue[index] = { ...queue[index], status: "uploading", error: undefined }; setRows([...queue]);
        try {
          const file = await encodeDocument(queue[index].file);
          if (!current()) return;
          const result = await client.request<{ vector_store: unknown; attachment: unknown; file: { id: string } }>("/v1/rag/ingest", { method: "POST", signal, maximumResponseBytes: 2 * 1024 * 1024, body: { file, vector_store_id: storeID, attributes: attrs, chunking_strategy: strategy } });
          const store = parseVectorStore(result.vector_store), attached = parseDocument(result.attachment, storeID);
          if (store.id !== storeID || !resourceID(result.file?.id) || attached.id !== result.file.id) throw new Error("Ingestion returned a mismatched store or file.");
          if (!current()) return;
          queue[index] = { ...queue[index], status: "attached", fileID: attached.id };
        } catch (cause) {
          if (!current()) return;
          queue[index] = { ...queue[index], status: "failed", error: cause instanceof Error ? cause.message : "Ingestion failed." };
          setRows([...queue]); onChanged(); throw cause;
        }
        setRows([...queue]);
      }
      if (current()) onChanged();
    });
  }
  const attached = rows.filter((row) => row.status === "attached").length;
  return <form className="form-card vector-upload-form" onSubmit={submit}><h2>Upload documents</h2><p>UTF-8 TXT, Markdown, CSV or JSON; 1 MiB per file, up to 20 files / 8 MiB per batch. PDF and binary documents need conversion first. Each successful file is ingested atomically; this batch is not one transaction.</p>
    <fieldset className="vector-form-fields" disabled={task.busy || attached > 0}><label>Documents<input type="file" multiple accept={uploadAccept} onChange={(event) => select(Array.from(event.target.files || []))} /></label><label>Document attributes JSON<textarea rows={3} value={attributes} onChange={(event) => setAttributes(event.target.value)} /></label><ChunkingFields value={chunking} onChange={setChunking} /></fieldset>
    {rows.length > 0 && <ul className="vector-upload-results">{rows.map((row, index) => <li key={index}><strong>{row.file.name}</strong> — {row.status === "pending" ? "Not attempted" : row.status}{row.fileID && <> · <code>{row.fileID}</code></>}{row.error && <span className="form-error"> · {row.error}</span>}</li>)}</ul>}
    {task.error && <p role="alert" className="form-error">{task.error}</p>}{attached > 0 && <p role="status">{attached} of {rows.length} documents confirmed attached. A failed or timed-out request may have completed; refresh documents before retrying.</p>}
    <div className="inline-actions"><button disabled={task.busy || !rows.length || attached === rows.length}>{task.busy ? "Ingesting…" : rows.some((row) => row.status === "failed") ? "Retry failed and remaining documents" : "Ingest documents"}</button>{task.busy && <button type="button" className="secondary" onClick={() => { stop.current = true; }}>Stop after current document</button>}{!task.busy && attached > 0 && <button type="button" className="secondary" onClick={() => { setRows([]); task.setError(""); }}>Start another upload</button>}<Link to={`?store=${encodeURIComponent(storeID)}&tab=documents`}>View documents</Link></div>
  </form>;
}
