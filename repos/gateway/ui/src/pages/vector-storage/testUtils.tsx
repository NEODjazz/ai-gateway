import { useEffect } from "react";
import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider, useAuth } from "../../auth/AuthContext";
import { VectorStoresPage } from "../VectorStoresPage";
import type { Document, VectorStore } from "./types";

export const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
export const store = (id = "vs_owned", name = "Product documentation"): VectorStore => ({ id, object: "vector_store", name, status: "completed", created_at: 1792022400, last_active_at: 1792022400, usage_bytes: 4, file_counts: { total: 1, completed: 1, in_progress: 0, failed: 0, cancelled: 0 }, metadata: { description: "Product manuals", region: "eu" } });
export const document = (id = "file_one", storeID = "vs_owned"): Document => ({ id, vector_store_id: storeID, filename: `${id}.txt`, status: "completed", usage_bytes: 4, created_at: 1792022400, attributes: { region: "eu" }, chunking_strategy: { type: "auto" } });
export const session = { user_id: "alice", credential_id: "ui-credential", roles: ["user"], capabilities: ["inference"] };
export type Call = { path: string; method: string; body?: Record<string, unknown>; headers: Headers; options?: RequestInit };

export function fixture(override?: (call: Call) => Response | Promise<Response> | undefined) {
  const calls: Call[] = [];
  const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
    const call: Call = { path: String(input), method: options?.method || "GET", body: options?.body ? JSON.parse(String(options.body)) : undefined, headers: new Headers(options?.headers), options };
    calls.push(call);
    const changed = override?.(call); if (changed !== undefined) return await changed;
    const path = new URL(call.path, "http://example.test").pathname;
    if (path === "/admin/v1/session") return json(session);
    if (path === "/v1/vector_stores/page") return json({ object: "list", data: [store()], total: 130, offset: Number(new URL(call.path,"http://example.test").searchParams.get("offset")), limit: 25 });
    if (path === "/v1/vector_stores/vs_owned" && call.method === "GET") return json(store());
    if (path === "/v1/vector_stores/vs_owned/files") return json({ data: [document()], has_more: false });
    if (path === "/v1/models") return json({ data: [{ id: "embedding-a" }, { id: "chat-a" }] });
    return json({ error: { message: `Unexpected ${call.method} ${call.path}` } }, 500);
  });
  return { calls, fetch };
}

function Gate() {
  const { session: current, restoreSession } = useAuth();
  useEffect(() => { if (!current) void restoreSession(); }, [current, restoreSession]);
  return current ? <VectorStoresPage /> : null;
}
export function show(query = "") {
  sessionStorage.setItem("ai-gateway.admin-token", "fixture-ui-credential");
  return render(<MemoryRouter initialEntries={["/vector-stores" + query]}><AuthProvider><Gate /></AuthProvider></MemoryRouter>);
}
