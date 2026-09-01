import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { AccessGroupsPage } from "./AccessGroupsPage";

const json = (payload: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } }));
const group = { id: "platform", name: "Platform", description: "Production platform", project_id: "core", allowed_models: ["gpt"], allowed_tools: ["toolset:weather"], tags: ["prod"], enabled: true };

function renderPage(path = "/access-groups") {
  sessionStorage.setItem("ai-gateway.admin-token", "token");
  return render(<MemoryRouter initialEntries={[path]}><AuthProvider><Routes><Route path="/access-groups" element={<AccessGroupsPage />} /><Route path="/access-groups/:id" element={<AccessGroupsPage />} /></Routes></AuthProvider></MemoryRouter>);
}

function mockReferences(url: string) {
  if (url === "/admin/v1/session") return json({ roles: ["admin"], capabilities: ["admin"] });
  if (url === "/admin/v1/projects") return json({ data: [{ id: "core", name: "Core", enabled: true }] });
  if (url === "/v1/models") return json({ data: [{ id: "gpt" }, { id: "embed" }] });
  if (url === "/admin/v1/mcp/toolsets") return json({ data: [{ id: "weather", name: "Weather", description: "Weather tools", tools: ["weather.current"], enabled: true }] });
  return undefined;
}

describe("AccessGroupsPage", () => {
  afterEach(() => { vi.restoreAllMocks(); sessionStorage.clear(); });

  it("opens a route-based detail with attached keys, budget exposure and impact-safe editing", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const url = String(input); const reference = mockReferences(url); if (reference) return reference;
      if (url === "/admin/v1/access-groups" && !options?.method) return json({ data: [group] });
      if (url === "/admin/v1/access-groups/platform" && !options?.method) return json(group);
      if (url.includes("/admin/v1/keys?") && url.includes("status=non_revoked")) return json({ data: [], total: 1, limit: 1, offset: 0 });
      if (url.includes("/admin/v1/keys?") && url.includes("access_group_id=platform")) return json({ data: [{ id: "vk_platform", alias: "platform-prod", organization_id: "acme", allowed_models: ["*"], created_at: "2026-09-01T10:00:00Z" }], total: 1, limit: 25, offset: 0, financials: { vk_platform: { policies: [{ policy: { currency: "USD", max_cost: 10 }, used_cost: 1, used_tokens: 12 }] } } });
      if (url === "/admin/v1/access-groups/platform" && options?.method === "PUT") return json(group);
      return json({ data: [] });
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Platform" }));
    expect(await screen.findByText("Immediate policy impact")).toBeInTheDocument();
    expect(await screen.findByText("platform-prod")).toBeInTheDocument();
    expect(screen.getByText("$1.00 / $10.00")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete" })).toBeDisabled();
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes("access_group_id=platform") && String(url).includes("expand=financials"))).toBe(true);

    await userEvent.click(screen.getByRole("button", { name: "Edit" }));
    const form = screen.getByRole("dialog", { name: "Edit access group" });
    expect(within(form).getByText(/Changes apply immediately to 1 non-revoked/)).toBeInTheDocument();
    await userEvent.clear(within(form).getByLabelText("Access group name"));
    await userEvent.type(within(form).getByLabelText("Access group name"), "Platform updated");
    await userEvent.click(within(form).getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, options]) => url === "/admin/v1/access-groups/platform" && options?.method === "PUT")).toBe(true));
    const update = fetchMock.mock.calls.find(([url, options]) => url === "/admin/v1/access-groups/platform" && options?.method === "PUT");
    expect(String(update?.[1]?.body)).toContain('"name":"Platform updated"');
    expect(String(update?.[1]?.body)).toContain('"allowed_models":["gpt"]');
  });

  it("creates a group from configured projects, models and MCP toolsets", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const url = String(input); const reference = mockReferences(url); if (reference) return reference;
      if (url === "/admin/v1/access-groups" && !options?.method) return json({ data: [] });
      if (url === "/admin/v1/access-groups/analytics" && options?.method === "PUT") return json({ ...group, id: "analytics", name: "Analytics" });
      if (url === "/admin/v1/access-groups/analytics") return json({ ...group, id: "analytics", name: "Analytics" });
      if (url.includes("/admin/v1/keys?")) return json({ data: [], total: 0, limit: 25, offset: 0, financials: {} });
      return json({ data: [] });
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Create Access Group" }));
    const form = screen.getByRole("dialog", { name: "Create access group" });
    await userEvent.type(within(form).getByLabelText("Access group ID"), "analytics");
    await userEvent.type(within(form).getByLabelText("Access group name"), "Analytics");
    await userEvent.selectOptions(within(form).getByLabelText("Access group project"), "core");
    await userEvent.click(within(form).getByLabelText("Models"));
    await userEvent.click(within(form).getByRole("option", { name: "gpt" }));
    await userEvent.click(within(form).getByLabelText("Tools"));
    await userEvent.click(within(form).getByRole("option", { name: /Weather toolset/ }));
    await userEvent.type(within(form).getByLabelText("Access group tags"), "prod, analytics");
    await userEvent.click(within(form).getByRole("button", { name: "Create access group" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, options]) => url === "/admin/v1/access-groups/analytics" && options?.method === "PUT")).toBe(true));
    const create = fetchMock.mock.calls.find(([url, options]) => url === "/admin/v1/access-groups/analytics" && options?.method === "PUT");
    const body = JSON.parse(String(create?.[1]?.body));
    expect(body).toMatchObject({ name: "Analytics", project_id: "core", allowed_models: ["gpt"], allowed_tools: ["toolset:weather"], tags: ["prod", "analytics"], enabled: true });
  });

  it("keeps attached keys available when budget projections are unavailable", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input); const reference = mockReferences(url); if (reference) return reference;
      if (url === "/admin/v1/access-groups") return json({ data: [group] });
      if (url === "/admin/v1/access-groups/platform") return json(group);
      if (url.includes("status=non_revoked")) return json({ data: [], total: 1, limit: 1, offset: 0 });
      if (url.includes("access_group_id=platform") && url.includes("expand=financials")) return json({ error: { code: "budget_unavailable", message: "Budget projections are unavailable" } }, 503);
      if (url.includes("access_group_id=platform")) return json({ data: [{ id: "vk_platform", alias: "platform-prod", created_at: "2026-09-01T10:00:00Z" }], total: 1, limit: 25, offset: 0 });
      return json({ data: [] });
    });
    renderPage("/access-groups/platform");
    expect(await screen.findByText("platform-prod")).toBeInTheDocument();
    expect(screen.getByText("No budget policy")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes("access_group_id=platform") && !String(url).includes("expand=financials") && !String(url).includes("status=non_revoked"))).toBe(true);
  });
});
