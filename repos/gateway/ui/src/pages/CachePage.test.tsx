import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { CachePage } from "./CachePage";

const json = (payload: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } }));
const diagnostics = {
  config: { exact_ttl_seconds: 60, exact_max_bytes: 1048576, semantic_ttl_seconds: 0, semantic_max_entries: 500, semantic_max_bytes: 2097152 },
  exact: { enabled: true, hits: 3, misses: 1, errors: 0, writes: 2, hit_ratio: 0.75 },
  semantic: { enabled: false, hits: 0, misses: 0, errors: 1, writes: 0, hit_ratio: 0 },
  operations: { get: { hit: 3, miss: 1 }, set: { ok: 2 }, semantic_get: { error: 1 } }
};

function renderPage() {
  sessionStorage.setItem("ai-gateway.admin-token", "token");
  return render(<MemoryRouter><AuthProvider><CachePage /></AuthProvider></MemoryRouter>);
}

describe("CachePage", () => {
  afterEach(() => { vi.restoreAllMocks(); sessionStorage.clear(); });

  it("separates exact, semantic and provider prompt-cache signals", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => String(input) === "/admin/v1/session" ? json({ roles: ["admin"], capabilities: ["admin"] }) : json(diagnostics));
    renderPage();
    expect(await screen.findByText("Exact cache")).toBeInTheDocument();
    expect(screen.getByText("Semantic cache")).toBeInTheDocument();
    expect(screen.getAllByText("75.0%").length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText("1 MiB")).toBeInTheDocument();
    expect(screen.getByText("500")).toBeInTheDocument();
    expect(screen.getByText(/Provider-reported cached input tokens/)).toBeInTheDocument();
    expect(screen.getByText("semantic_get")).toBeInTheDocument();
    expect(screen.getByText("error")).toBeInTheDocument();
    expect(screen.getByText(/deployment-managed/)).toBeInTheDocument();
  });

  it("refreshes current-replica counters without exposing a settings mutation", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => String(input) === "/admin/v1/session" ? json({ roles: ["admin"], capabilities: ["admin"] }) : json(diagnostics));
    renderPage();
    await screen.findByText("Exact cache");
    const header = screen.getByRole("heading", { name: "Caching" }).closest("header")!;
    await userEvent.click(within(header).getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(fetchMock.mock.calls.filter(([url]) => url === "/admin/v1/cache/diagnostics")).toHaveLength(2));
    expect(screen.queryByRole("button", { name: /save cache/i })).not.toBeInTheDocument();
  });
});
