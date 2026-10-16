import { useState, type FormEvent } from "react";
import { ModalFrame } from "../../components/ModalFrame";
import { parseMetadata, parseVectorStore, type VectorStore } from "./types";
import { useStorageClient, useStorageTask } from "./runtime";

export function StoreForm({ initial, onClose, onSaved, inline = false }: { initial?: VectorStore; onClose: () => void; onSaved: (store: VectorStore) => void; inline?: boolean }) {
  const client = useStorageClient(), task = useStorageTask();
  const [name, setName] = useState(initial?.name || ""), [description, setDescription] = useState(initial?.metadata.description || "");
  const [metadata, setMetadata] = useState(JSON.stringify(Object.fromEntries(Object.entries(initial?.metadata || {}).filter(([key]) => key !== "description")), null, 2));
  const [expiry, setExpiry] = useState(initial?.expires_after ? String(initial.expires_after.days) : "");
  function submit(event: FormEvent) {
    event.preventDefault();
    void task.run(async (signal, current) => {
      const fields = parseMetadata(JSON.parse(metadata));
      if ("description" in fields) throw new Error("Use the Description field to edit description metadata.");
      const merged = { ...fields }; if (description.trim()) merged.description = description.trim(); else delete merged.description;
      parseMetadata(merged);
      const trimmed = name.trim();
      if (!trimmed || Array.from(trimmed).length > 256) throw new Error("Name needs 1–256 characters.");
      if (initial?.expires_after && !expiry) throw new Error("An existing expiry policy can be changed to 1–365 days, but cannot be cleared by this API.");
      const days = Number(expiry);
      if (expiry && (!Number.isInteger(days) || days < 1 || days > 365)) throw new Error("Expiry must be 1–365 days.");
      const value = parseVectorStore(await client.request(initial ? `/v1/vector_stores/${encodeURIComponent(initial.id)}` : "/v1/vector_stores", { method: "POST", signal, body: { name: trimmed, metadata: merged, ...(expiry ? { expires_after: { anchor: "last_active_at", days } } : {}) } }));
      if (initial && value.id !== initial.id) throw new Error("The server returned a different vector store.");
      if (current()) onSaved(value);
    });
  }
  const content = <section className={inline ? "form-card vector-store-create" : "modal directory-form"}><div className="modal-heading"><h2>{initial ? "Edit vector store" : "Create vector store"}</h2>{!inline && <button type="button" className="icon-button" aria-label="Close vector store form" disabled={task.busy} onClick={onClose}>×</button>}</div>
    <form onSubmit={submit}><fieldset className="vector-form-fields form-grid" disabled={task.busy}>
      <label className="span-2">Name<input required maxLength={256} value={name} onChange={(event) => setName(event.target.value)} /></label>
      <label className="span-2">Description<textarea maxLength={512} value={description} onChange={(event) => setDescription(event.target.value)} /></label>
      <label className="span-2">Additional metadata JSON<textarea rows={5} spellCheck={false} value={metadata} onChange={(event) => setMetadata(event.target.value)} /></label>
      <label className="span-2">Expire after inactive days<input type="number" min="1" max="365" step="1" placeholder="No expiry" value={expiry} onChange={(event) => setExpiry(event.target.value)} /></label>
    </fieldset><p className="muted">Description is stored in metadata. Expiry is relative to last activity. This store belongs to the active credential and user.</p>
    {task.error && <p className="form-error" role="alert">{task.error}</p>}<div className="modal-actions"><button type="button" className="secondary" disabled={task.busy} onClick={onClose}>Cancel</button><button disabled={task.busy}>{task.busy ? "Saving…" : initial ? "Save vector store" : "Create vector store"}</button></div></form>
  </section>;
  return inline ? content : <ModalFrame label={initial ? "Edit vector store" : "Create vector store"} onClose={onClose} dismissDisabled={task.busy}>{content}</ModalFrame>;
}

export function DeleteStoreForm({ store, onClose, onDeleted }: { store: VectorStore; onClose: () => void; onDeleted: () => void }) {
  const client = useStorageClient(), task = useStorageTask();
  return <ModalFrame label="Delete vector store" onClose={onClose} dismissDisabled={task.busy}><section className="modal compact-modal"><h2>Delete vector store</h2><p>Delete <strong>{store.name}</strong> ({store.id})? This removes the store and its document attachments. Source files remain in file storage. This action cannot be undone.</p>{task.error && <p className="form-error" role="alert">{task.error}</p>}<div className="modal-actions"><button className="secondary" disabled={task.busy} onClick={onClose}>Cancel</button><button className="danger" disabled={task.busy} onClick={() => void task.run(async (signal, current) => {
    const result = await client.request<{ id: string; deleted: boolean }>(`/v1/vector_stores/${encodeURIComponent(store.id)}`, { method: "DELETE", signal });
    if (result?.id !== store.id || result.deleted !== true) throw new Error("Deletion was not confirmed by the server.");
    if (current()) onDeleted();
  })}>{task.busy ? "Deleting…" : "Confirm delete"}</button></div></section></ModalFrame>;
}
