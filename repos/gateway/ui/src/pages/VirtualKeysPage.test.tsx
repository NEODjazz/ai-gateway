import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { VirtualKeysPage } from "./VirtualKeysPage";

const key = { id: "vk_alpha", alias: "production", description: "Production key", user_id: "user-1", team_id: "team-1", roles: ["operator"], access_group_ids: ["platform"], allowed_models: ["gpt"], allowed_tools: ["search"], rate_limit_rpm: 60, rate_limit_tpm: 1200, tags: ["prod"], expires_at: "2026-09-27T10:00:00Z", created_at: "2026-08-27T10:00:00Z" };
const json = (value: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } }));

function mockAPI(keyRows: unknown[] = [key], userRows: unknown[] = [{ id: "user-1", name: "Alice", email: "alice@example.com", team_ids: ["team-1"], status: "active" }], teamRows: unknown[] = [{ id: "team-1", name: "Platform", status: "active" }], organizationRows: unknown[] = [{ id: "org-1", name: "Acme", team_ids: ["team-1"], status: "active" }], accessGroupRows: unknown[] = [{ id: "platform", name: "Platform access", project_id: "core", allowed_models: ["gpt"], allowed_tools: ["search"], enabled: true }]) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
    const path = String(input);
    if (path === "/admin/v1/keys" && options?.method === "POST") return json({ id: "vk_new", token: "sk-ag-secret-once" });
    if (path.endsWith("/rotate") && options?.method === "POST") return json({ id: "vk_rotated", token: "sk-ag-rotated-once" });
    if (path.includes("/admin/v1/keys/vk_alpha") && options?.method === "DELETE") return new Response(null, { status: 204 });
		if (path.includes("/admin/v1/keys/vk_alpha") && options?.method) return json({});
		if (path.includes("/admin/v1/keys?")) {
			const url = new URL(path, "http://gateway.test");
			const userTeams = new Map(userRows.map((row) => [String((row as { id: string }).id), ((row as { team_ids?: string[] }).team_ids || [])]));
			const organizationTeams = organizationRows.find((row) => (row as { id: string }).id === url.searchParams.get("organization_id")) as { team_ids?: string[] } | undefined;
			let rows = [...keyRows] as Array<{ id: string; alias?: string; organization_id?: string; team_id?: string; user_id?: string; revoked_at?: string; disabled_at?: string; expires_at?: string; created_at: string }>;
			const search = url.searchParams.get("search")?.toLowerCase();
			if (search) rows = rows.filter((row) => (row.alias || "").toLowerCase().includes(search));
			const team = url.searchParams.get("team_id");
			if (team) rows = rows.filter((row) => row.team_id === team || Boolean(row.user_id && userTeams.get(row.user_id)?.includes(team)));
			const organization = url.searchParams.get("organization_id");
			if (organization) rows = rows.filter((row) => row.organization_id === organization || Boolean(row.team_id && organizationTeams?.team_ids?.includes(row.team_id)) || Boolean(row.user_id && userTeams.get(row.user_id)?.some((id) => organizationTeams?.team_ids?.includes(id))));
			const user = url.searchParams.get("user_id"); if (user) rows = rows.filter((row) => row.user_id === user);
			const keyID = url.searchParams.get("key_id")?.toLowerCase(); if (keyID) rows = rows.filter((row) => row.id.toLowerCase().includes(keyID));
			const status = url.searchParams.get("status"); if (status) rows = rows.filter((row) => row.revoked_at ? status === "revoked" : row.disabled_at ? status === "disabled" : row.expires_at && new Date(row.expires_at) <= new Date() ? status === "expired" : status === "active");
			const sortBy = url.searchParams.get("sort_by") || "created"; const direction = url.searchParams.get("sort_order") === "asc" ? 1 : -1;
			rows.sort((left, right) => direction * String(sortBy === "key" ? left.id : sortBy === "team" ? left.team_id || "" : sortBy === "user" ? left.user_id || "" : sortBy === "alias" ? left.alias || "" : left.created_at).localeCompare(String(sortBy === "key" ? right.id : sortBy === "team" ? right.team_id || "" : sortBy === "user" ? right.user_id || "" : sortBy === "alias" ? right.alias || "" : right.created_at)));
			const total = rows.length; const limit = Number(url.searchParams.get("limit") || 25); const offset = Number(url.searchParams.get("offset") || 0); const pageRows = rows.slice(offset, offset + limit);
			const financials = Object.fromEntries(pageRows.map((row) => [row.id, row.id === "vk_alpha" ? { key_id: row.id, policies: [{ policy: { id: 7, scope_type: "key", scope_id: "vk_alpha", period: "month", currency: "USD", max_cost: 100, enabled: true }, window_start: "2026-09-01T00:00:00Z", window_end: "2026-10-01T00:00:00Z", used_cost: 12.5, remaining_cost: 87.5, used_tokens: 150 }] } : { key_id: row.id, policies: [] }]));
			return json({ data: pageRows, total, limit, offset, financials });
		}
		if (path.includes("/admin/v1/users")) return json({ data: userRows });
		if (path.includes("/admin/v1/teams")) return json({ data: teamRows });
		if (path.includes("/admin/v1/organizations")) return json({ data: organizationRows });
    if (path === "/admin/v1/access-groups") return json({ data: accessGroupRows });
    if (path === "/v1/models") return json({ data: [{ id: "gpt" }, { id: "embed" }] });
    if (path === "/admin/v1/usage/report?days=30") return json({ by_key: [
      { name: "vk_alpha", currency: "USD", requests: 2, total_tokens: 120, cost: 12.5 },
      { name: "vk_alpha", currency: "EUR", requests: 1, total_tokens: 30, cost: 3 }
    ] });
    return json({});
  });
}
function renderPage() { sessionStorage.setItem("ai-gateway.admin-token", "token"); render(<MemoryRouter><AuthProvider><VirtualKeysPage /></AuthProvider></MemoryRouter>); }

describe("VirtualKeysPage", () => {
  afterEach(() => { vi.restoreAllMocks(); sessionStorage.clear(); window.history.replaceState({}, "", "/"); });

  it("opens a key deep link from access-group details", async () => {
    window.history.replaceState({}, "", "/ui/api-keys?key_id=vk_alpha");
    const fetchMock = mockAPI(); renderPage();
    expect(await screen.findByText("production")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([path]) => String(path).includes("key_id=vk_alpha"))).toBe(true);
  });

  it("opens the existing edit form from the route-based details workspace", async () => {
    window.history.replaceState({}, "", "/ui/api-keys?key_id=vk_alpha&edit=1");
    mockAPI(); renderPage();
    const dialog = await screen.findByRole("dialog", { name: "Edit virtual key" });
    expect(within(dialog).getByLabelText("Alias")).toHaveValue("production");
  });

  it("renders searchable, sortable keys with icon refresh and resettable filters", async () => {
    const fetchMock = mockAPI(); renderPage();
    expect(await screen.findByText("production")).toBeInTheDocument();
    expect(screen.getByText("key:vk_alpha · $100.00 / month")).toBeInTheDocument();
    expect(screen.getByText("€3.00 · $12.50")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create Virtual Key" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Columns" }));
    expect(screen.getByRole("menuitemcheckbox", { name: "Key" })).toHaveAttribute("aria-checked", "true");
    const descriptionColumn = screen.getByRole("menuitemcheckbox", { name: "Description" });
    expect(descriptionColumn).toHaveAttribute("aria-checked", "false");
    await userEvent.click(descriptionColumn);
    await userEvent.click(screen.getByRole("menuitemcheckbox", { name: "Requests (30d)" }));
    await userEvent.click(screen.getByRole("menuitemcheckbox", { name: "Tokens (30d)" }));
    await userEvent.click(screen.getByRole("menuitemcheckbox", { name: "Budget used" }));
    await userEvent.click(screen.getByRole("menuitemcheckbox", { name: "Budget remaining" }));
    await userEvent.click(screen.getByRole("menuitemcheckbox", { name: "Budget reset" }));
    expect(screen.getByRole("columnheader", { name: "Description" })).toBeInTheDocument();
    expect(screen.getByText("Production key")).toBeInTheDocument();
    const keyRow = screen.getByText("production").closest("tr");
    expect(keyRow).not.toBeNull();
    expect(within(keyRow!).getByText("150")).toBeInTheDocument();
    expect(within(keyRow!).getByText("3")).toBeInTheDocument();
    expect(within(keyRow!).getByText("key:vk_alpha · $12.50")).toBeInTheDocument();
    expect(within(keyRow!).getByText("key:vk_alpha · $87.50")).toBeInTheDocument();
    expect(within(keyRow!).getByText("2026-10-01 00:00:00 UTC")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh virtual keys" })).toHaveTextContent("");
    for (const heading of ["Key", "Team", "User", "Created"]) expect(screen.getByRole("button", { name: new RegExp(`^${heading}$`) })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Budgets" })).toHaveAttribute("title", expect.stringContaining("simultaneously"));
    await userEvent.type(screen.getByLabelText("Search keys by alias"), "missing");
    expect(await screen.findByText(/No virtual keys match/)).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([path]) => String(path).includes("search=missing"))).toBe(true);
    await userEvent.clear(screen.getByLabelText("Search keys by alias"));
    expect(await screen.findByText("production")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Filter" }));
    const dialog = await screen.findByRole("dialog", { name: "Filter virtual keys" });
    await userEvent.selectOptions(within(dialog).getByLabelText("Organization"), "org-1");
    await userEvent.selectOptions(within(dialog).getByLabelText("Team"), "team-1");
    await userEvent.click(within(dialog).getByRole("button", { name: "Reset filters" }));
    expect(await screen.findByText("production")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Filter" }));
    const statusDialog = await screen.findByRole("dialog", { name: "Filter virtual keys" });
    await userEvent.selectOptions(within(statusDialog).getByLabelText("Status"), "active");
    await userEvent.click(within(statusDialog).getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path]) => String(path).includes("status=active"))).toBe(true));
    await userEvent.click(screen.getByRole("button", { name: "Refresh virtual keys" }));
    await waitFor(() => expect(fetchMock.mock.calls.filter(([path]) => String(path).includes("/admin/v1/keys?")).length).toBeGreaterThan(1));
    expect(fetchMock.mock.calls.some(([path]) => path === "/admin/v1/usage/report?days=30")).toBe(true);
    expect(fetchMock.mock.calls.some(([path]) => String(path).includes("expand=financials"))).toBe(true);
    expect(fetchMock.mock.calls.some(([path]) => path === "/admin/v1/budgets")).toBe(false);
  });

	it("creates a team-owned key without requiring a user and uses the model multi-select", async () => {
    const fetchMock = mockAPI(); const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    renderPage(); await screen.findByText("production");
    await userEvent.click(screen.getByRole("button", { name: "Create Virtual Key" }));
    const form = await screen.findByRole("dialog", { name: "Create Virtual Key" });
		await userEvent.type(within(form).getByLabelText("Alias"), "automation");
		await userEvent.selectOptions(within(form).getByLabelText("Organization"), "org-1");
		await userEvent.selectOptions(within(form).getByLabelText("Team"), "team-1");
		expect(within(form).getByLabelText("User")).not.toBeRequired();
		await userEvent.click(within(form).getByLabelText("Access groups"));
		await userEvent.click(within(form).getByRole("option", { name: /Platform access/ }));
		await userEvent.click(within(form).getByLabelText("Models"));
		await userEvent.click(within(form).getByRole("option", { name: "gpt" }));
		await userEvent.click(within(form).getByRole("option", { name: "embed" }));
		expect(within(form).getByText("gpt")).toBeInTheDocument();
		expect(within(form).getByRole("button", { name: "Remove model embed" })).toBeInTheDocument();
    await userEvent.click(within(form).getByRole("button", { name: "Generate key" }));
    const issued = await screen.findByRole("dialog", { name: "Virtual key created" });
    expect(within(issued).getByDisplayValue("sk-ag-secret-once")).toBeInTheDocument();
    await userEvent.click(within(issued).getByRole("button", { name: "Copy" }));
    expect(await within(issued).findByText("Copied to clipboard")).toBeInTheDocument();
    expect(writeText).toHaveBeenCalledWith("sk-ag-secret-once");
    const createCall = fetchMock.mock.calls.find(([path, options]) => path === "/admin/v1/keys" && options?.method === "POST")!;
    const body = JSON.parse(String(createCall[1]?.body));
		expect(body).toMatchObject({ alias: "automation", team_id: "team-1" });
		expect(body.access_group_ids).toEqual(["platform"]);
		expect(body.allowed_models).toEqual(expect.arrayContaining(["gpt", "embed"]));
		expect(body).not.toHaveProperty("user_id");
		expect(body).not.toHaveProperty("organization");
		expect(body).not.toHaveProperty("organization_id");
	});

	it("filters team-owned and member-owned keys by the selected team", async () => {
		const memberKey = { id: "vk_member", alias: "platform-member", user_id: "user-platform", allowed_models: [], created_at: "2026-08-28T10:00:00Z" };
		const otherKey = { id: "vk_other", alias: "other-team", user_id: "user-other", allowed_models: [], created_at: "2026-08-28T09:00:00Z" };
		mockAPI([memberKey, otherKey], [
			{ id: "user-platform", name: "Platform User", email: "platform@example.com", team_ids: ["platform-e2e"], status: "active" },
			{ id: "user-other", name: "Other User", email: "other@example.com", team_ids: ["other"], status: "active" }
		], [{ id: "platform-e2e", name: "Platform E2E", status: "active" }], [{ id: "org-e2e", name: "E2E", team_ids: ["platform-e2e"], status: "active" }]);
		renderPage(); expect(await screen.findByText("platform-member")).toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Filter" }));
		const dialog = await screen.findByRole("dialog", { name: "Filter virtual keys" });
		await userEvent.selectOptions(within(dialog).getByLabelText("Team"), "platform-e2e");
		await userEvent.click(within(dialog).getByRole("button", { name: "Apply filters" }));
		expect(screen.getByText("platform-member")).toBeInTheDocument();
		expect(screen.queryByText("other-team")).not.toBeInTheDocument();
	});

	it("creates an organization-owned key when no team or user is selected", async () => {
		const fetchMock = mockAPI(); renderPage(); await screen.findByText("production");
		await userEvent.click(screen.getByRole("button", { name: "Create Virtual Key" }));
		const form = await screen.findByRole("dialog", { name: "Create Virtual Key" });
		await userEvent.type(within(form).getByLabelText("Alias"), "organization-automation");
		await userEvent.selectOptions(within(form).getByLabelText("Organization"), "org-1");
		await userEvent.click(within(form).getByRole("button", { name: "Generate key" }));
		await screen.findByRole("dialog", { name: "Virtual key created" });
		const createCall = fetchMock.mock.calls.find(([path, options]) => path === "/admin/v1/keys" && options?.method === "POST")!;
		const body = JSON.parse(String(createCall[1]?.body));
		expect(body).toMatchObject({ alias: "organization-automation", organization_id: "org-1" });
		expect(body).not.toHaveProperty("team_id");
		expect(body).not.toHaveProperty("user_id");
	});

	it("paginates keys with preset and custom row counts", async () => {
		const manyKeys = Array.from({ length: 30 }, (_, index) => ({ id: `vk_${index}`, alias: `key-${index}`, user_id: "user-1", allowed_models: [], created_at: `2026-08-27T10:${String(index).padStart(2, "0")}:00Z` }));
		mockAPI(manyKeys); renderPage(); await screen.findByText("key-29");
		expect(screen.getAllByRole("row")).toHaveLength(26);
		await userEvent.selectOptions(screen.getByLabelText("Rows per page"), "10");
		expect(screen.getAllByRole("row")).toHaveLength(11);
		expect(screen.getByText("1–10 of 30")).toBeInTheDocument();
		await userEvent.selectOptions(screen.getByLabelText("Rows per page"), "custom");
		await userEvent.clear(screen.getByLabelText("Custom rows per page"));
		await userEvent.type(screen.getByLabelText("Custom rows per page"), "7");
		expect(screen.getAllByRole("row")).toHaveLength(8);
	});

  it("preserves edit, rotation, status and revoke operations", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const fetchMock = mockAPI(); renderPage(); await screen.findByText("production");
    await userEvent.click(screen.getByRole("button", { name: "Actions for production" }));
    expect(screen.getByRole("menuitem", { name: "Inspect" })).toHaveAttribute("href", "/api-keys/vk_alpha");
    await userEvent.click(screen.getByRole("menuitem", { name: "Edit" }));
    const edit = await screen.findByRole("dialog", { name: "Edit virtual key" });
    await userEvent.type(within(edit).getByLabelText("Description"), "Updated policy");
    await userEvent.click(within(edit).getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path, options]) => path === "/admin/v1/keys/vk_alpha" && options?.method === "PUT")).toBe(true));
    await userEvent.click(screen.getByRole("button", { name: "Actions for production" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Disable" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path]) => path === "/admin/v1/keys/vk_alpha/disable")).toBe(true));
    await userEvent.click(screen.getByRole("button", { name: "Actions for production" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Rotate" }));
    const rotated = await screen.findByRole("dialog", { name: "Virtual key created" });
    expect(within(rotated).getByDisplayValue("sk-ag-rotated-once")).toBeInTheDocument();
    await userEvent.click(within(rotated).getByRole("button", { name: "Close" }));
    await userEvent.click(screen.getByRole("button", { name: "Actions for production" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Revoke" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path, options]) => path === "/admin/v1/keys/vk_alpha" && options?.method === "DELETE")).toBe(true));
  });
});
