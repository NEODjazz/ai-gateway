import { useEffect, useRef, useState } from "react";
import { GatewayButton } from "../../components/GatewayButton";
import type { PlaygroundConnection } from "./requests";

export type OutputFile = { fileID: string; containerID?: string; filename: string };
const downloadLimit = 32 * 1024 * 1024;
const previewLimit = 8 * 1024 * 1024;

export function outputFilename(value: string): string {
  return (value.split(/[\\/]/).pop() || "response-file").replace(/[\u0000-\u001f\u007f<>:"|?*]/g, "_").slice(0, 128) || "response-file";
}

export function outputFilePath(file: OutputFile, responseID: string, stored: boolean): string {
  const valid = (id: string) => id !== "." && id !== ".." && /^[A-Za-z0-9_.-]{1,256}$/.test(id);
  if (!valid(file.fileID)) throw new Error("Invalid file ID.");
  if (!file.containerID) return `/v1/files/${file.fileID}/content`;
  if (!stored || !valid(responseID) || !valid(file.containerID)) throw new Error("Generated files require a stored Responses result. Use store: true.");
  return `/v1/responses/${responseID}/containers/${file.containerID}/files/${file.fileID}/content`;
}

export async function checkedImageType(blob: Blob, contentType: string): Promise<string> {
  const type = contentType.split(";")[0].trim().toLowerCase();
  const bytes = new Uint8Array(await blob.slice(0, 12).arrayBuffer());
  const prefix = (...values: number[]) => values.every((value, index) => bytes[index] === value);
  const safe = type === "image/png" && prefix(137, 80, 78, 71, 13, 10, 26, 10)
    || type === "image/jpeg" && prefix(255, 216, 255)
    || type === "image/gif" && (prefix(71, 73, 70, 56, 55, 97) || prefix(71, 73, 70, 56, 57, 97))
    || type === "image/webp" && prefix(82, 73, 70, 70) && bytes[8] === 87 && bytes[9] === 69 && bytes[10] === 66 && bytes[11] === 80;
  if (!safe) throw new Error("Preview supports verified PNG, JPEG, GIF and WebP images only. Download other file types.");
  return type;
}

export function FileResults({ files, connection, responseID, stored, disabled = false }: {
  files: OutputFile[]; connection?: PlaygroundConnection; responseID: string; stored: boolean; disabled?: boolean;
}) {
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState<{ text: string; error?: boolean }>();
  const [preview, setPreview] = useState<{ url: string; name: string }>();
  const current = useRef<AbortController | undefined>(undefined);
  const previewURL = useRef("");
  const fileScope = JSON.stringify(files);
  function clearPreview() {
    if (previewURL.current) URL.revokeObjectURL(previewURL.current);
    previewURL.current = ""; setPreview(undefined);
  }
  useEffect(() => {
    setBusy(""); setNotice(undefined); setPreview(undefined);
    return () => {
      current.current?.abort(); current.current = undefined;
      if (previewURL.current) URL.revokeObjectURL(previewURL.current);
      previewURL.current = "";
    };
  }, [connection, disabled, responseID, stored, fileScope]);

  async function load(file: OutputFile, image: boolean) {
    if (!connection || disabled || current.current) return;
    const controller = new AbortController(); current.current = controller;
    setBusy(file.fileID); setNotice(undefined); clearPreview();
    try {
      const path = outputFilePath(file, responseID, stored);
      const result = await connection.client.requestBinary(connection.path(path), { method: "GET", signal: controller.signal, maximumResponseBytes: image ? previewLimit : downloadLimit });
      if (current.current !== controller || controller.signal.aborted) return;
      if (result.body.size > (image ? previewLimit : downloadLimit)) throw new Error("Response file exceeds the download limit.");
      const type = image ? await checkedImageType(result.body, result.contentType) : "application/octet-stream";
      if (current.current !== controller || controller.signal.aborted) return;
      const url = URL.createObjectURL(new Blob([result.body], { type }));
      const name = outputFilename(file.filename);
      if (image) { previewURL.current = url; setPreview({ url, name }); }
      else {
        const link = document.createElement("a"); link.href = url; link.download = name; document.body.appendChild(link);
        try { link.click(); setNotice({ text: `Download started: ${name}` }); }
        finally { link.remove(); URL.revokeObjectURL(url); }
      }
    } catch (cause) {
      if (current.current === controller && !controller.signal.aborted) setNotice({ error: true, text: cause instanceof Error ? cause.message : "Could not read response file." });
    } finally {
      if (current.current === controller) { current.current = undefined; setBusy(""); }
    }
  }

  return <section aria-label="Response files">
    <strong>Files</strong>
    <p className="muted">Downloads use the current Gateway credential. Generated files require a stored response; expired or inaccessible files can fail. Download limit: 32 MiB; image preview: 8 MiB.</p>
    <ul>{files.map((file) => <li key={`${file.containerID || ""}:${file.fileID}`}>
      <span>{outputFilename(file.filename)}</span> <small>{file.fileID}</small>
      <div className="playground-actions">
        <GatewayButton view="flat" disabled={disabled || !connection || !!busy || !!file.containerID && (!stored || !responseID)} onClick={() => void load(file, false)} aria-label={`Download ${outputFilename(file.filename)}`}>Download</GatewayButton>
        <GatewayButton view="flat" disabled={disabled || !connection || !!busy || !!file.containerID && (!stored || !responseID)} onClick={() => void load(file, true)} aria-label={`Preview ${outputFilename(file.filename)}`}>Preview image</GatewayButton>
      </div>
    </li>)}</ul>
    {busy && <GatewayButton view="flat" onClick={() => { current.current?.abort(); current.current = undefined; setBusy(""); setNotice({ text: "File request cancelled." }); }}>Cancel file request</GatewayButton>}
    {notice && <p role={notice.error ? "alert" : "status"}>{notice.text}</p>}
    {preview && <figure><img className="playground-generated-image" src={preview.url} alt={preview.name} /><GatewayButton view="flat" onClick={clearPreview}>Close preview</GatewayButton></figure>}
  </section>;
}
