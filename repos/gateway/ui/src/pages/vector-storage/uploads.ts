export const uploadAccept = ".txt,.md,.markdown,.csv,.json,text/plain,text/markdown,text/csv,application/json";
export const uploadMaxBytes = 1024 * 1024;
const contentTypes: Record<string, string> = { txt: "text/plain", md: "text/markdown", markdown: "text/markdown", csv: "text/csv", json: "application/json" };

export function validateUploads(files: File[]) {
  if (!files.length || files.length > 20 || files.reduce((sum, file) => sum + file.size, 0) > 8 * 1024 * 1024) throw new Error("Select 1–20 documents, at most 8 MiB total.");
  if (files.some((file) => !contentTypes[file.name.split(".").pop()?.toLowerCase() || ""] || file.size < 1 || file.size > uploadMaxBytes)) throw new Error("Documents must be UTF-8 TXT, Markdown, CSV or JSON, 1 byte–1 MiB each. PDF and binary documents require document conversion before ingestion.");
}

export async function encodeDocument(file: File) {
  validateUploads([file]);
  const buffer = await new Promise<ArrayBuffer>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result as ArrayBuffer);
    reader.onerror = () => reject(new Error("Could not read document."));
    reader.onabort = () => reject(new Error("Document read was cancelled."));
    reader.readAsArrayBuffer(file);
  });
  const bytes = new Uint8Array(buffer);
  const text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  if (!text.trim() || text.includes("\0")) throw new Error("A document must contain nonempty UTF-8 text without NUL bytes.");
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return { filename: file.name, content: btoa(binary), content_type: contentTypes[file.name.split(".").pop()!.toLowerCase()] };
}
