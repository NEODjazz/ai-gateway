import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { VirtualKeysPage } from "./VirtualKeysPage";

const key = { id: "vk_alpha", alias: "production", description: "Production key", user_id: "user-1", team_id: "team-1", roles: ["operator"], allowed_models: ["gpt"], allowed_tools: ["search"], rate_limit_rpm: 60, rate_limit_tpm: 1200, tags: ["prod"], expires_at: "2026-09-27T10:00:00Z", created_at: "2026-08-27T10:00:00Z" };
const json = (value: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } }));

function mockAPI(keyRows: unknown[] = [key], userRows: unknown[] = [{ id: "user-1", name: "Alice", email: "alice@example.com", team_ids: ["team-1"], status: "active" }], teamRows: unknown[] = [{ id: "team-1", name: "Platform", status: "active" }], organizationRows: unknown[] = [{ id: "org-1", name: "Acme", team_ids: ["team-1"], status: "active" }]) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
    const path = String(input);
    if (path === "/admin/v1/keys" && options?.method === "POST") return json({ id: "vk_new", token: "sk-ag-secret-once" });
    if (path.endsWith("/rotate") && options?.method === "POST") return json({ id: "vk_rotated", token: "sk-ag-rotated-once" });
    if (path.includes("/admin/v1/keys/vk_alpha") && options?.method === "DELETE") return new Response(null, { status: 204 });
    if (path.includes("/admin/v1/keys/vk_alpha") && options?.method) return json({});
		if (path.includes("/admin/v1/keys?")) return json({ data: keyRows });
		if (path.includes("/admin/v1/users")) return json({ data: userRows });
		if (path.includes("/admin/v1/teams")) return json({ data: teamRows });
		if (path.includes("/admin/v1/organizations")) return json({ data: organizationRows });
    if (path === "/v1/models") return json({ data: [{ id: "gpt" }, { id: "embed" }] });
    if (path === "/admin/v1/budgets") return json({ data: [{ id: 7, scope_type: "key", scope_id: "vk_alpha", period: "month", currency: "USD", max_cost: 100, enabled: true }] });
    if (path === "/admin/v1/usage/report?days=30") return json({ by_key: [
      { name: "vk_alpha", currency: "USD", requests: 2, total_tokens: 120, cost: 12.5 },
      { name: "vk_alpha", currency: "EUR", requests: 1, total_tokens: 30, cost: 3 }
    ] });
    return json({});
  });
}
function renderPage() { sessionStorage.setItem("ai-gateway.admin-token", "token"); render(<AuthProvider><VirtualKeysPage /></AuthProvider>); }

describe("VirtualKeysPage", () => {
  it("renders searchable, sortable keys with icon refresh and resettable filters", async () => {
    const fetchMock = mockAPI(); renderPage();
    expect(await screen.findByText("production")).toBeInTheDocument();
    expect(screen.getByText("100 USD / month")).toBeInTheDocument();
    expect(screen.getByText("€3.00 · $12.50")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create Virtual Key" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Columns" }));
    expect(screen.getByRole("menuitemcheckbox", { name: "Key" })).toHaveAttribute("aria-checked", "true");
    const descriptionColumn = screen.getByRole("menuitemcheckbox", { name: "Description" });
    expect(descriptionColumn).toHaveAttribute("aria-checked", "false");
    await userEvent.click(descriptionColumn);
    await userEvent.click(screen.getByRole("menuitemcheckbox", { name: "Requests (30d)" }));
    await userEvent.click(screen.getByRole("menuitemcheckbox", { name: "Tokens (30d)" }));
    expect(screen.getByRole("columnheader", { name: "Description" })).toBeInTheDocument();
    expect(screen.getByText("Production key")).toBeInTheDocument();
    const keyRow = screen.getByText("production").closest("tr");
    expect(keyRow).not.toBeNull();
    expect(within(keyRow!).getByText("150")).toBeInTheDocument();
    expect(within(keyRow!).getByText("3")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh virtual keys" })).toHaveTextContent("");
    for (const heading of ["Key", "Team", "User", "Created", "Budget"]) expect(screen.getByRole("button", { name: new RegExp(`^${heading}$`) })).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Search keys by alias"), "missing");
    expect(screen.getByText(/No virtual keys match/)).toBeInTheDocument();
    await userEvent.clear(screen.getByLabelText("Search keys by alias"));
    await userEvent.click(screen.getByRole("button", { name: "Filter" }));
    const dialog = await screen.findByRole("dialog", { name: "Filter virtual keys" });
    await userEvent.selectOptions(within(dialog).getByLabelText("Organization"), "org-1");
    await userEvent.selectOptions(within(dialog).getByLabelText("Team"), "team-1");
    await userEvent.click(within(dialog).getByRole("button", { name: "Reset filters" }));
    expect(await screen.findByText("production")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Budget/ }));
    await userEvent.click(screen.getByRole("button", { name: "Refresh virtual keys" }));
    await waitFor(() => expect(fetchMock.mock.calls.filter(([path]) => String(path).includes("/admin/v1/keys?")).length).toBeGreaterThan(1));
    expect(fetchMock.mock.calls.some(([path]) => path === "/admin/v1/usage/report?days=30")).toBe(true);
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
