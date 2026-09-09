import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { VirtualKeyDetailsPage } from "./VirtualKeyDetailsPage";

const json = (payload: unknown, status = 200) => Promise.resolve(new Response(status === 204 ? null : JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } }));
function LocationProbe() { const location = useLocation(); return <div>Policy route {location.search}</div>; }

describe("VirtualKeyDetailsPage", () => {
  afterEach(() => { vi.restoreAllMocks(); sessionStorage.clear(); });

  it("joins ownership, grants, scoped usage and budget policies and preserves lifecycle actions", async () => {
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: vi.fn().mockResolvedValue(undefined) } });
    let disabledAt = "";
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const path = String(input);
      if (path.includes("/admin/v1/keys?key_id=vk_alpha")) return json({ data: [{ id: "vk_alpha", alias: "production", description: "Production automation", user_id: "user-1", roles: ["developer"], access_group_ids: ["platform"], allowed_models: ["gpt-5"], allowed_tools: ["mcp.search.*"], rate_limit_rpm: 60, rate_limit_tpm: 1200, tags: ["prod"], disabled_at: disabledAt || undefined, created_at: "2026-08-27T10:00:00Z" }], financials: { vk_alpha: { key_id: "vk_alpha", policies: [{ policy: { id: 7, scope_type: "key", scope_id: "vk_alpha", period: "month", currency: "USD", max_cost: 100, enabled: true }, window_start: "2026-09-01T00:00:00Z", window_end: "2026-10-01T00:00:00Z", used_cost: 12.5, remaining_cost: 87.5, used_tokens: 0 }] } } });
      if (path === "/admin/v1/usage/report?days=30&scope_type=key&scope_id=vk_alpha") return json({ totals: [{ currency: "USD", requests: 10, errors: 1, input_tokens: 80, output_tokens: 20, total_tokens: 100, cache_hits: 2, cost: 1.25, avg_latency_ms: 120 }], daily: [{ date: "2026-09-01", currency: "USD", requests: 10, errors: 1, input_tokens: 80, output_tokens: 20, total_tokens: 100, cache_hits: 2, cost: 1.25, avg_latency_ms: 120 }] });
      if (path === "/admin/v1/users?limit=500") return json({ data: [{ id: "user-1", name: "Alice" }] });
      if (path === "/admin/v1/teams?limit=500" || path === "/admin/v1/organizations?limit=500") return json({ data: [] });
      if (path === "/admin/v1/access-groups") return json({ data: [{ id: "platform", name: "Platform access" }] });
      if (path === "/admin/v1/policy-attachments/resolve" && init?.method === "POST") return json({ matched_attachments: [{ id: "prod-dlp", policy_name: "strict", scope: "specific", matched_via: ["key", "model", "tag"], policy_status: "enabled", dlp: true, output_dlp: false, av: false }], effective_policies: ["strict"], dlp: true, output_dlp: false, av: false, enforceable: true, issues: [] });
      if (path.endsWith("/rotate") && init?.method === "POST") return json({ id: "vk_alpha", token: "sk-rotated-once" });
      if (path.endsWith("/disable") && init?.method === "POST") { disabledAt = "2026-09-02T01:00:00Z"; return json({}); }
      return json({ error: { message: `unexpected ${init?.method || "GET"} ${path}` } }, 500);
    });
    render(<MemoryRouter initialEntries={["/api-keys/vk_alpha"]}><AuthProvider><Routes><Route path="/api-keys/:id" element={<VirtualKeyDetailsPage />} /><Route path="/api-keys" element={<div>Edit catalog</div>} /></Routes></AuthProvider></MemoryRouter>);
    expect(await screen.findByRole("heading", { name: "production" })).toBeInTheDocument();
    expect(screen.getByText(/User: Alice/)).toBeInTheDocument();
    expect(screen.getByText("Platform access")).toBeInTheDocument();
    expect(screen.getByText("$1.25")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([path]) => String(path).includes("scope_type=key&scope_id=vk_alpha"))).toBe(true);

    await userEvent.click(screen.getByRole("tab", { name: "Policies" }));
    expect(screen.getByLabelText("Policy impact model")).toHaveValue("gpt-5");
    await userEvent.click(screen.getByRole("button", { name: "Resolve policy impact" }));
    const policyResult = await screen.findByLabelText("Policy resolution result");
    expect(policyResult).toHaveTextContent("prod-dlp");
    expect(policyResult).toHaveTextContent("Enforceable");
    const resolutionCall = fetchMock.mock.calls.find(([path, init]) => path === "/admin/v1/policy-attachments/resolve" && init?.method === "POST")!;
    expect(JSON.parse(String(resolutionCall[1]?.body))).toEqual({ credential_id: "vk_alpha", credential_alias: "production", model: "gpt-5", tags: ["prod"] });

    await userEvent.click(screen.getByRole("tab", { name: "Usage & budgets" }));
    expect(screen.getByText("key:vk_alpha")).toBeInTheDocument();
    expect(screen.getByText("$87.50")).toBeInTheDocument();
    expect(screen.getByText("2026-09-01")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Actions for production" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Rotate" }));
    const rotated = await screen.findByRole("dialog", { name: "Virtual key rotated" });
    expect(within(rotated).getByDisplayValue("sk-rotated-once")).toBeInTheDocument();
    await userEvent.click(within(rotated).getByRole("button", { name: "Copy" }));
    expect(await within(rotated).findByText("Copied to clipboard")).toBeInTheDocument();
    await userEvent.click(within(rotated).getByRole("button", { name: "Close" }));

    await userEvent.click(screen.getByRole("button", { name: "Actions for production" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Disable" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path, init]) => String(path).endsWith("/disable") && init?.method === "POST")).toBe(true));
    expect(await screen.findByText("disabled")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("tab", { name: "Settings" }));
    await userEvent.click(screen.getByRole("button", { name: "Edit settings" }));
    expect(await screen.findByText("Edit catalog")).toBeInTheDocument();
  });

  it("revokes with confirmation and returns to the key catalog", async () => {
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const path = String(input);
      if (path.includes("/admin/v1/keys?")) return json({ data: [{ id: "vk_alpha", alias: "production", organization_id: "org-1", created_at: "2026-08-27T10:00:00Z" }], financials: {} });
      if (path.includes("/admin/v1/usage/report")) return json({ totals: [], daily: [] });
      if (path.includes("/admin/v1/organizations")) return json({ data: [{ id: "org-1", name: "Acme" }] });
      if (path.includes("/admin/v1/users") || path.includes("/admin/v1/teams") || path.includes("/admin/v1/access-groups")) return json({ data: [] });
      if (path === "/admin/v1/keys/vk_alpha" && init?.method === "DELETE") return json({}, 204);
      return json({}, 500);
    });
    render(<MemoryRouter initialEntries={["/api-keys/vk_alpha"]}><AuthProvider><Routes><Route path="/api-keys/:id" element={<VirtualKeyDetailsPage />} /><Route path="/api-keys" element={<div>Virtual key catalog</div>} /></Routes></AuthProvider></MemoryRouter>);
    await screen.findByRole("heading", { name: "production" });
    await userEvent.click(screen.getByRole("button", { name: "Actions for production" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Revoke" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path, init]) => path === "/admin/v1/keys/vk_alpha" && init?.method === "DELETE")).toBe(true));
    expect(await screen.findByText("Virtual key catalog")).toBeInTheDocument();
  });

  it("opens a prefilled key attachment workflow", async () => {
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const path = String(input);
      if (path.includes("/admin/v1/keys?")) return json({ data: [{ id: "vk_alpha", alias: "clinical prod", team_id: "care-a", tags: ["hipaa"], allowed_models: ["*"], created_at: "2026-08-27T10:00:00Z" }], financials: {} });
      if (path.includes("/admin/v1/usage/report")) return json({ totals: [], daily: [] });
      if (path.includes("/admin/v1/users") || path.includes("/admin/v1/teams") || path.includes("/admin/v1/organizations") || path.includes("/admin/v1/access-groups")) return json({ data: [] });
      return json({}, 500);
    });
    render(<MemoryRouter initialEntries={["/api-keys/vk_alpha"]}><AuthProvider><Routes><Route path="/api-keys/:id" element={<VirtualKeyDetailsPage />} /><Route path="/policies" element={<LocationProbe />} /></Routes></AuthProvider></MemoryRouter>);
    await screen.findByRole("heading", { name: "clinical prod" });
    await userEvent.click(screen.getByRole("tab", { name: "Policies" }));
    expect(screen.getByLabelText("Policy impact model")).toHaveValue("");
    await userEvent.click(screen.getByRole("button", { name: "Policy actions for clinical prod" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Create key attachment" }));
    expect(await screen.findByText("Policy route ?create=1&attach_key=vk_alpha&suggested_id=key-clinical-prod")).toBeInTheDocument();
  });
});
