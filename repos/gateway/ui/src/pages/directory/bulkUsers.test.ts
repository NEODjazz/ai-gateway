import { parseBulkUsers } from "./bulkUsers";

it("parses BOM, CRLF, commas and escaped quotes and generates stable preview IDs", () => {
  const id = vi.fn(() => "generated-id");
  expect(parseBulkUsers('\uFEFFid,name,email,external_id\r\nalice,"Alice, A.",alice@example.test,subject-1\r\n,"Bob ""B""",bob@example.test,\r\n', id)).toEqual([
    { id: "alice", name: "Alice, A.", email: "alice@example.test", external_id: "subject-1" },
    { id: "generated-id", name: 'Bob "B"', email: "bob@example.test", external_id: "" }
  ]);
  expect(id).toHaveBeenCalledTimes(1);
});

it.each([
  ["empty input", ""],
  ["header only", "id,name"],
  ["unsupported column", "id,password\na,secret"],
  ["duplicate header", "id,id\na,b"],
  ["missing identity header", "name\nAlice"],
  ["missing identity", "id,name,email\n,Alice,"],
  ["wrong column count", "id,name\na,Alice,extra"],
  ["unclosed quote", 'id,name\na,"Alice'],
  ["misplaced quote", 'id,name\na,Al"ice'],
  ["characters after quote", 'id,name\na,"Alice"trailer'],
  ["invalid ID", "id\ninvalid/id"],
  ["duplicate ID", "id\nalice\nalice"],
  ["invalid email", "email\ninvalid"],
  ["control characters", 'id,name\na,"Alice\nNew line"'],
  ["overlong UTF-8 field", `id,name\na,${"я".repeat(129)}`],
  ["too many rows", `id\n${Array.from({ length: 101 }, (_, i) => `u-${i}`).join("\n")}`],
  ["oversized source", "x".repeat(256 * 1024 + 1)]
])("rejects %s before any creation", (_label, source) => expect(() => parseBulkUsers(source)).toThrow());

it("accepts 100 users with explicit IDs and blank lines", () => {
  const rows = parseBulkUsers(`id\n\n${Array.from({ length: 100 }, (_, i) => `u-${i}`).join("\n")}\n\n`);
  expect(rows).toHaveLength(100);
  expect(rows[99].id).toBe("u-99");
});
