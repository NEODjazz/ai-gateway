import { useState } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../../auth/AuthContext";
import { DirectoryPicker } from "./DirectoryPicker";
import type { DirectoryIdentity } from "./IdentityForm";

const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
it("pages and searches the full directory while retaining an explicit selection", async () => {
  const paths: string[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = String(input); paths.push(path); const q = new URL(path, "http://example.test").searchParams;
    const offset = Number(q.get("offset"));
    const rows = q.get("search") ? [{ id: "user-620", name: "Last identity", status: "active" }] : Array.from({ length: 25 }, (_, i) => ({ id: `user-${offset + i + 1}`, name: `User ${offset + i + 1}`, status: "active" }));
    return json({ data: rows, total: q.get("search") ? 1 : 620, offset, limit: 25 });
  });
  function Fixture() { const [value, setValue] = useState<DirectoryIdentity>(); return <DirectoryPicker kind="users" label="Team member user" selected={value} onChange={setValue} />; }
  render(<AuthProvider><Fixture /></AuthProvider>);
  await screen.findByRole("option", { name: "User 1 · user-1" });
  await userEvent.click(screen.getByRole("button", { name: "Next user choices" }));
  await userEvent.selectOptions(screen.getByLabelText("Team member user"), (await screen.findByRole("option", { name: "User 26 · user-26" })).getAttribute("value")!);
  await userEvent.type(screen.getByRole("searchbox", { name: "Search users to add" }), "Last");
  expect(await screen.findByRole("option", { name: "Last identity · user-620" })).toBeInTheDocument();
  expect(screen.getByLabelText("Team member user")).toHaveValue("user-26");
  expect(screen.getByRole("option", { name: "User 26 · user-26" })).toBeInTheDocument();
  await waitFor(() => expect(paths.some((path) => path.includes("search=Last") && path.includes("offset=0") && path.includes("status=active"))).toBe(true));
  expect(paths.some((path) => path.includes("limit=500"))).toBe(false);
});
