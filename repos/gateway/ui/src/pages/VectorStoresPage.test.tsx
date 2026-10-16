import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { fixture, json, show, store } from "./vector-storage/testUtils";

describe("Vector store directory", () => {
  it("does not display an invented empty total when the initial directory fails", async () => {
    fixture((call) => call.path.startsWith("/v1/vector_stores/page") ? json({ error: { message: "Directory offline" } }, 503) : undefined);
    show();
    await screen.findByText("Directory offline");
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    expect(screen.queryByText(/of 0/)).not.toBeInTheDocument();
  });

  it("uses complete server totals, search, paging and supported sorting without global inventories", async () => {
    const { calls } = fixture((call) => {
      if (!call.path.startsWith("/v1/vector_stores/page")) return;
      const q = new URL(call.path, "http://example.test").searchParams;
      return json({ object: "list", data: [store(q.get("offset") === "25" ? "vs_late" : "vs_owned", q.get("search") || "Product documentation")], total: 130, offset: Number(q.get("offset")), limit: Number(q.get("limit")) });
    });
    show(); await screen.findByText("1–25 of 130");
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    await screen.findByText("vs_late"); expect(screen.getByText("26–50 of 130")).toBeInTheDocument();
    await userEvent.type(screen.getByRole("searchbox"), "Needle");
    await screen.findByRole("link", { name: "Needle" });
    await userEvent.selectOptions(screen.getByLabelText("Status"), "expired");
    await userEvent.click(screen.getByRole("button", { name: /^Name/ }));
    await waitFor(() => expect(calls.some((call) => call.path.includes("search=Needle") && call.path.includes("status=expired") && call.path.includes("sort_by=name") && call.path.includes("offset=0"))).toBe(true));
    expect(calls.some((call) => call.path.startsWith("/admin/") && !call.path.endsWith("/session"))).toBe(false);
    expect(screen.queryByRole("button", { name: /^Storage.*↕/ })).not.toBeInTheDocument();
  });

  it("ignores delayed searches and retains a marked previous page when a refresh fails", async () => {
    let resolveOld!: (response: Response) => void;
    fixture((call) => {
      if (!call.path.startsWith("/v1/vector_stores/page")) return;
      const q = new URL(call.path, "http://example.test").searchParams;
      if (q.get("search") === "x") return new Promise((resolve) => { resolveOld = resolve; });
      if (q.get("offset") === "25") return json({ error: { message: "Directory unavailable" } }, 503);
      return json({ object: "list", data: [store("vs_owned", q.get("search") === "xy" ? "Fresh store" : "Product documentation")], total: 130, offset: 0, limit: 25 });
    });
    show(); await screen.findByText("Product documentation");
    await userEvent.type(screen.getByRole("searchbox"), "x"); await waitFor(() => expect(resolveOld).toBeTypeOf("function"));
    await userEvent.type(screen.getByRole("searchbox"), "y"); await screen.findByText("Fresh store");
    resolveOld(json({ object: "list", data: [store("vs_stale", "Old store")], total: 999, offset: 0, limit: 25 }));
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    await screen.findByText("Directory unavailable");
    expect(screen.getByText("Fresh store")).toBeInTheDocument(); expect(screen.queryByText("Old store")).not.toBeInTheDocument();
    expect(screen.getByText("Showing the last successful directory page.")).toBeInTheDocument();
  });

  it("preserves failed creation drafts and forwards metadata and expiry before opening document upload", async () => {
    let conflict = true;
    const { calls } = fixture((call) => {
      if (call.path === "/v1/vector_stores" && call.method === "POST") return conflict ? json({ error: { message: "Storage unavailable" } }, 503) : json(store("vs_owned", "New collection"));
    });
    show(); await screen.findByText("Product documentation");
    await userEvent.click(screen.getByRole("button", { name: "Create vector store" }));
    await userEvent.type(screen.getByLabelText("Name", { exact: true }), "New collection");
    await userEvent.type(screen.getByLabelText("Description", { exact: true }), "Manuals");
    await userEvent.clear(screen.getByLabelText("Additional metadata JSON"));
    fireEvent.change(screen.getByLabelText("Additional metadata JSON"), { target: { value: '{"region":"eu"}' } });
    await userEvent.type(screen.getByLabelText("Expire after inactive days"), "7");
    await userEvent.click(screen.getByRole("button", { name: "Create vector store" }));
    await screen.findByText("Storage unavailable"); expect(screen.getByLabelText("Name", { exact: true })).toHaveValue("New collection");
    expect(calls.find((call) => call.method === "POST")?.body).toEqual({ name: "New collection", metadata: { region: "eu", description: "Manuals" }, expires_after: { anchor: "last_active_at", days: 7 } });
    conflict = false;
    await userEvent.click(screen.getByRole("button", { name: "Create vector store" }));
    expect(await screen.findByRole("heading", { name: "Upload documents" })).toBeInTheDocument();
  });

  it("requires confirmed deletion and retains its dialog after a server failure", async () => {
    let fail = true;
    const { calls } = fixture((call) => call.method === "DELETE" ? fail ? json({ error: { message: "Delete failed" } }, 503) : json({ id: "vs_owned", deleted: true }) : undefined);
    show(); await screen.findByText("Product documentation");
    await userEvent.click(screen.getByRole("button", { name: "Actions for vector store vs_owned" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    const dialog = screen.getByRole("dialog", { name: "Delete vector store" });
    expect(calls.some((call) => call.method === "DELETE")).toBe(false);
    await userEvent.click(within(dialog).getByRole("button", { name: "Confirm delete" }));
    await screen.findByText("Delete failed"); expect(dialog).toBeInTheDocument();
    fail = false; await userEvent.click(within(dialog).getByRole("button", { name: "Confirm delete" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(await screen.findByText("Vector store deleted. Source files were retained.")).toBeInTheDocument();
    expect(calls.some((call) => call.method === "DELETE" && call.path.startsWith("/v1/files"))).toBe(false);
  });

  it("isolates a virtual-key workspace and does not persist the key or expire the UI session on a key error", async () => {
    const expired = vi.fn(); window.addEventListener("control-plane-session-expired", expired);
    try {
      const { calls } = fixture((call) => call.headers.get("Authorization") === "Bearer virtual-fixture" ? json({ error: { message: "Selected key is invalid" } }, 401) : undefined);
      show(); await screen.findByText("Product documentation");
      await userEvent.selectOptions(screen.getByLabelText("Resource credential"), "key");
      expect(screen.queryByText("Product documentation")).not.toBeInTheDocument();
      await userEvent.type(screen.getByLabelText("Virtual key", { exact: true }), "virtual-fixture");
      await userEvent.click(screen.getByRole("button", { name: "Use virtual key" }));
      await screen.findByText("Selected key is invalid");
      expect(expired).not.toHaveBeenCalled(); expect(sessionStorage.getItem("ai-gateway.admin-token")).toBe("fixture-ui-credential");
      expect(Object.values(sessionStorage)).not.toContain("virtual-fixture");
      expect(calls.find((call) => call.headers.get("Authorization") === "Bearer virtual-fixture")?.options?.credentials).toBe("omit");
    } finally { window.removeEventListener("control-plane-session-expired", expired); }
  });
});
