export type Metadata = Record<string, string>;
export type Attributes = Record<string, string | number | boolean>;
export type Chunking = { type: "auto" } | { type: "static"; static: { max_chunk_size_tokens: number; chunk_overlap_tokens: number } };
export type VectorStore = { id: string; object: "vector_store"; name: string; status: "completed" | "expired"; created_at: number; last_active_at: number; usage_bytes: number; file_counts: { total: number; completed: number; in_progress: number; failed: number; cancelled: number }; metadata: Metadata; expires_at?: number; expires_after?: { anchor: "last_active_at"; days: number } };
export type DirectoryPage = { object: "list"; data: VectorStore[]; total: number; offset: number; limit: number };
export type Document = { id: string; vector_store_id: string; filename?: string; status: string; usage_bytes: number; created_at: number; attributes: Attributes; chunking_strategy: Chunking };
export type DocumentPage = { data: Document[]; has_more: boolean; last_id?: string };
export type SearchResult = { file_id: string; filename: string; score: number; attributes: Attributes; content: { type: "text"; text: string }[] };
export type SearchResponse = { data: SearchResult[]; has_more: boolean };
export const resourceID = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9_.-]{1,128}$/.test(value);
export const object = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value);
export const count = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) >= 0;
const length = (value: string) => Array.from(value).length;
const date = (value: unknown): value is number => count(value) && Number.isFinite(new Date(value * 1000).valueOf());

export function parseMetadata(value: unknown): Metadata {
  if (!object(value) || Object.keys(value).length > 16 || Object.entries(value).some(([key, item]) => length(key) > 64 || typeof item !== "string" || length(item) > 512)) throw new Error("Metadata must be an object with at most 16 string entries, 64-character keys and 512-character values.");
  return value as Metadata;
}
export function parseAttributes(value: unknown): Attributes {
  if (!object(value) || Object.keys(value).length > 16 || Object.entries(value).some(([key, item]) => !key || length(key) > 64 || !(typeof item === "string" && length(item) <= 512 || typeof item === "boolean" || typeof item === "number" && Number.isFinite(item)))) throw new Error("Attributes need at most 16 scalar entries: strings, finite numbers or booleans.");
  return value as Attributes;
}
export function parseChunking(value: unknown): Chunking {
  if (!object(value)) throw new Error("Invalid chunking settings.");
  if (value.type === "auto" && value.static === undefined) return { type: "auto" };
  const s = value.static;
  if (value.type === "static" && object(s) && Number.isInteger(s.max_chunk_size_tokens) && Number(s.max_chunk_size_tokens) >= 100 && Number(s.max_chunk_size_tokens) <= 4096 && count(s.chunk_overlap_tokens) && s.chunk_overlap_tokens <= Number(s.max_chunk_size_tokens) / 2) return value as Chunking;
  throw new Error("Static chunk size must be 100–4096 tokens, with overlap no greater than half.");
}
export function parseVectorStore(value: unknown): VectorStore {
  if (!object(value) || !resourceID(value.id) || value.object !== "vector_store" || typeof value.name !== "string" || !value.name.trim() || !["completed", "expired"].includes(String(value.status)) || !count(value.created_at) || !count(value.last_active_at) || !count(value.usage_bytes) || !object(value.file_counts) || ["total", "completed", "in_progress", "failed", "cancelled"].some((key) => !count((value.file_counts as Record<string, unknown>)[key])) || value.expires_at !== undefined && !count(value.expires_at)) throw new Error("Vector store returned invalid metadata.");
  parseMetadata(value.metadata);
  if (!date(value.created_at) || !date(value.last_active_at) || value.expires_at !== undefined && !date(value.expires_at)) throw new Error("Vector store returned invalid dates.");
  const counts = value.file_counts as Record<string, number>;
  if (counts.total !== counts.completed + counts.in_progress + counts.failed + counts.cancelled) throw new Error("Vector store returned inconsistent document counts.");
  if (value.expires_after !== undefined && (!object(value.expires_after) || value.expires_after.anchor !== "last_active_at" || !Number.isInteger(value.expires_after.days) || Number(value.expires_after.days) < 1 || Number(value.expires_after.days) > 365)) throw new Error("Vector store returned invalid expiry policy.");
  return value as VectorStore;
}
export function parseDirectoryPage(value: unknown): DirectoryPage {
  if (!object(value) || value.object !== "list" || !Array.isArray(value.data) || !count(value.total) || !count(value.offset) || !count(value.limit) || value.limit < 1 || value.limit > 200 || value.data.length > value.limit || value.data.length > value.total) throw new Error("Vector store directory returned an invalid page.");
  const data = value.data.map(parseVectorStore);
  if (new Set(data.map((item) => item.id)).size !== data.length) throw new Error("Vector store directory returned duplicate IDs.");
  return { ...value, data } as DirectoryPage;
}
export function parseDocument(value: unknown, store: string): Document {
  if (!object(value) || !resourceID(value.id) || value.vector_store_id !== store || !["completed", "in_progress", "failed", "cancelled"].includes(String(value.status)) || !count(value.created_at) || !count(value.usage_bytes) || value.filename !== undefined && typeof value.filename !== "string") throw new Error("Document returned invalid or mismatched metadata.");
  parseAttributes(value.attributes); parseChunking(value.chunking_strategy);
  if (!date(value.created_at)) throw new Error("Document returned an invalid date.");
  return value as Document;
}
export function parseDocumentPage(value: unknown, store: string): DocumentPage {
  if (!object(value) || !Array.isArray(value.data) || value.data.length > 100 || typeof value.has_more !== "boolean") throw new Error("Documents returned an invalid page.");
  const data = value.data.map((item) => parseDocument(item, store));
  if (new Set(data.map((item) => item.id)).size !== data.length || value.has_more && (!data.length || value.last_id !== data[data.length - 1].id)) throw new Error("Document pagination did not advance.");
  return { data, has_more: value.has_more, last_id: typeof value.last_id === "string" ? value.last_id : undefined };
}
export function parseSearch(value: unknown): SearchResponse {
  if (!object(value) || !Array.isArray(value.data) || value.data.length > 50 || value.has_more !== false || value.data.some((item) => !object(item) || !resourceID(item.file_id) || typeof item.filename !== "string" || typeof item.score !== "number" || !Number.isFinite(item.score) || !Array.isArray(item.content) || item.content.some((part) => !object(part) || part.type !== "text" || typeof part.text !== "string"))) throw new Error("Search returned invalid results.");
  for (const item of value.data) parseAttributes(item.attributes);
  return value as SearchResponse;
}
export const timestamp = (value?: number) => value === undefined ? "—" : new Date(value * 1000).toISOString().replace("T", " ").replace(".000Z", " UTC");
export function bytes(value: number) { return value < 1024 ? `${value} B` : value < 1024 * 1024 ? `${(value / 1024).toFixed(1)} KiB` : `${(value / 1024 / 1024).toFixed(1)} MiB`; }
