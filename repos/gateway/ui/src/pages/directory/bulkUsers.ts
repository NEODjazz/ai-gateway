export type BulkUser = { id: string; name: string; email: string; external_id: string };
const columns = ["id", "name", "email", "external_id"];

// Accept quoted commas and escaped quotes, but reject malformed input before
// issuing any creates. The size and row limits bound parser memory.
export function parseBulkUsers(source: string, createID: () => string = () => `user-${crypto.randomUUID()}`): BulkUser[] {
  if (new TextEncoder().encode(source).length > 256 * 1024) throw new Error("CSV exceeds 256 KiB.");
  const rows: string[][] = [];
  let row: string[] = [], field = "", quoted = false, closed = false;
  const finishField = () => { row.push(field.trim()); field = ""; closed = false; };
  const finishRow = () => {
    finishField();
    if (row.some(Boolean)) rows.push(row);
    row = [];
    if (rows.length > 101) throw new Error("A batch can contain at most 100 users.");
  };
  const csv = source.replace(/^\uFEFF/, "");
  for (let index = 0; index < csv.length; index++) {
    const character = csv[index];
    if (quoted) {
      if (character === '"') {
        if (csv[index + 1] === '"') { field += '"'; index++; }
        else { quoted = false; closed = true; }
      } else field += character;
    } else if (character === ",") finishField();
    else if (character === "\n" || character === "\r") { if (character === "\r" && csv[index + 1] === "\n") index++; finishRow(); }
    else if (character === '"') { if (field || closed) throw new Error("CSV contains a misplaced quote."); quoted = true; }
    else { if (closed) throw new Error("CSV contains text after a closing quote."); field += character; }
  }
  if (quoted) throw new Error("CSV contains an unclosed quoted field.");
  if (field || row.length || closed) finishRow();
  const header = rows.shift();
  if (!header?.length || header.some((key) => !columns.includes(key)) || new Set(header).size !== header.length || !header.some((key) => key === "id" || key === "email")) throw new Error("CSV needs unique id and/or email headers. Supported columns: id, name, email, external_id.");
  if (!rows.length) throw new Error("CSV has no users.");
  const ids = new Set<string>();
  return rows.map((values, index) => {
    const fail = (message: string): never => { throw new Error(`User row ${index + 1}: ${message}`); };
    if (values.length !== header.length) fail("column count does not match the header.");
    const value = Object.fromEntries(header.map((key, position) => [key, values[position]]));
    if (!value.id && !value.email) fail("id or email is required.");
    const user: BulkUser = { id: value.id || createID(), name: value.name || "", email: value.email || "", external_id: value.external_id || "" };
    if (!/^[A-Za-z0-9._@:-]{1,256}$/.test(user.id)) fail("id must use 1–256 letters, digits or ._@:- characters.");
    for (const key of columns as (keyof BulkUser)[]) {
      if (new TextEncoder().encode(user[key]).length > (key === "email" ? 320 : 256) || /[\x00-\x1f\x7f]/.test(user[key])) fail(`${key} is too long or contains control characters.`);
    }
    if (user.email && !/^[^\s@]+@[^\s@]+$/.test(user.email)) fail("email is invalid.");
    if (ids.has(user.id)) fail(`duplicate id ${user.id}.`);
    ids.add(user.id);
    return user;
  });
}
