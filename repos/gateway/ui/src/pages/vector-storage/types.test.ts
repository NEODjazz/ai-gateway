import { document, store } from "./testUtils";
import { parseAttributes, parseChunking, parseDirectoryPage, parseDocument, parseMetadata, parseSearch, parseVectorStore } from "./types";
import { encodeDocument, validateUploads } from "./uploads";

it("rejects incomplete or contradictory resource metadata rather than showing plausible zero counts", () => {
  expect(() => parseVectorStore({ ...store(), usage_bytes: undefined })).toThrow();
  expect(() => parseVectorStore({ ...store(), created_at: Number.MAX_SAFE_INTEGER })).toThrow();
  expect(() => parseVectorStore({ ...store(), file_counts: { ...store().file_counts, total: 2 } })).toThrow();
  expect(() => parseDirectoryPage({ object: "list", data: [store(), store()], total: 2, offset: 0, limit: 25 })).toThrow();
  expect(() => parseDocument(document("file_one", "foreign"), "vs_owned")).toThrow();
});

it("keeps scalar constraints and refuses unsupported nested or invalid chunking values", () => {
  expect(parseAttributes({ region: "eu", rank: 3, approved: true })).toEqual({ region: "eu", rank: 3, approved: true });
  expect(() => parseAttributes({ nested: { value: 1 } })).toThrow();
  expect(() => parseAttributes({ number: Infinity })).toThrow();
  expect(() => parseMetadata({ nested: 3 })).toThrow();
  expect(() => parseMetadata(Object.fromEntries(Array.from({ length: 17 }, (_, i) => [String(i), "x"])))).toThrow();
  expect(() => parseChunking({ type: "static", static: { max_chunk_size_tokens: 100, chunk_overlap_tokens: 51 } })).toThrow();
  expect(() => parseChunking({ type: "auto", static: { max_chunk_size_tokens: 800 } })).toThrow();
});

it("rejects invalid search result scores and foreign value shapes", () => {
  expect(parseSearch({ data: [], has_more: false })).toEqual({ data: [], has_more: false });
  expect(() => parseSearch({ data: [{ file_id: "file_one", filename: "doc.txt", score: Infinity, attributes: {}, content: [] }], has_more: false })).toThrow();
  expect(() => parseSearch({ data: [], has_more: true })).toThrow();
});

it("bounds uploads and preserves UTF-8 document bytes without accepting binary content", async () => {
  const text = "Документ о Gateway";
  const encoded = await encodeDocument(new File([text], "guide.md", { type: "text/markdown" }));
  expect(encoded.filename).toBe("guide.md");
  expect(new TextDecoder().decode(Uint8Array.from(atob(encoded.content), (c) => c.charCodeAt(0)))).toBe(text);
  expect(() => validateUploads([new File(["pdf"], "scan.pdf")])).toThrow(/conversion/);
  expect(() => validateUploads([new File([new Uint8Array(1024 * 1024 + 1)], "big.txt")])).toThrow();
  await expect(encodeDocument(new File([new Uint8Array([0xff])], "invalid.txt"))).rejects.toThrow();
  await expect(encodeDocument(new File(["bad\0text"], "bad.txt"))).rejects.toThrow(/NUL/);
});
