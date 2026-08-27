import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { VirtualKeysPage } from "./VirtualKeysPage";

const key = { id: "vk_alpha", alias: "production", user_id: "user-1", team_id: "team-1", allowed_models: ["gpt"], created_at: "2026-08-27T10:00:00Z" };
const json = (value: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } }));

function mockAPI() {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
    const path = String(input);
    if (path === "/admin/v1/keys" && options?.method === "POST") return json({ id: "vk_new", token: "sk-ag-secret-once" });
    if (path.endsWith("/rotate") && options?.method === "POST") return json({ id: "vk_rotated", token: "sk-ag-rotated-once" });
    if (path.includes("/admin/v1/keys/vk_alpha") && options?.method === "DELETE") return new Response(null, { status: 204 });
    if (path.includes("/admin/v1/keys/vk_alpha") && options?.method) return json({});
    if (path.includes("/admin/v1/keys?")) return json({ data: [key] });
    if (path.includes("/admin/v1/users")) return json({ data: [{ id: "user-1", name: "Alice", email: "alice@example.com", team_ids: ["team-1"], status: "active" }] });
    if (path.includes("/admin/v1/teams")) return json({ data: [{ id: "team-1", name: "Platform", status: "active" }] });
    if (path.includes("/admin/v1/organizations")) return json({ data: [{ id: "org-1", name: "Acme", team_ids: ["team-1"], status: "active" }] });
    if (path === "/v1/models") return json({ data: [{ id: "gpt" }, { id: "embed" }] });
    if (path === "/admin/v1/budgets") return json({ data: [{ id: 7, scope_type: "key", scope_id: "vk_alpha", period: "month", currency: "USD", max_cost: 100, enabled: true }] });
    return json({});
  });
}
function renderPage() { sessionStorage.setItem("ai-gateway.admin-token", "token"); render(<AuthProvider><VirtualKeysPage /></AuthProvider>); }

describe("VirtualKeysPage", () => {
  it("renders searchable, sortable keys with icon refresh and resettable filters", async () => {
    const fetchMock = mockAPI(); renderPage();
    expect(await screen.findByText("production")).toBeInTheDocument();
    expect(screen.getByText("100 USD / month")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create Virtual Key" })).toBeInTheDocument();
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
  });

  it("uses reference selectors and shows the generated token once with copy confirmation", async () => {
    const fetchMock = mockAPI(); const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    renderPage(); await screen.findByText("production");
    await userEvent.click(screen.getByRole("button", { name: "Create Virtual Key" }));
    const form = await screen.findByRole("dialog", { name: "Create Virtual Key" });
    await userEvent.type(within(form).getByLabelText("Alias"), "automation");
    await userEvent.selectOptions(within(form).getByLabelText("Organization"), "org-1");
    await userEvent.selectOptions(within(form).getByLabelText("Team"), "team-1");
    await userEvent.selectOptions(within(form).getByLabelText("User"), "user-1");
    await userEvent.selectOptions(within(form).getByLabelText("Models"), ["gpt", "embed"]);
    await userEvent.click(within(form).getByRole("button", { name: "Generate key" }));
    const issued = await screen.findByRole("dialog", { name: "Virtual key created" });
    expect(within(issued).getByDisplayValue("sk-ag-secret-once")).toBeInTheDocument();
    await userEvent.click(within(issued).getByRole("button", { name: "Copy" }));
    expect(await within(issued).findByText("Copied to clipboard")).toBeInTheDocument();
    expect(writeText).toHaveBeenCalledWith("sk-ag-secret-once");
    const createCall = fetchMock.mock.calls.find(([path, options]) => path === "/admin/v1/keys" && options?.method === "POST")!;
    const body = JSON.parse(String(createCall[1]?.body));
    expect(body).toMatchObject({ alias: "automation", team_id: "team-1", user_id: "user-1" });
    expect(body.allowed_models).toEqual(expect.arrayContaining(["gpt", "embed"]));
    expect(body).not.toHaveProperty("organization");
  });

  it("preserves edit, rotation, status and revoke operations", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const fetchMock = mockAPI(); renderPage(); await screen.findByText("production");
    await userEvent.click(screen.getByRole("button", { name: "Edit" }));
    const edit = await screen.findByRole("dialog", { name: "Edit virtual key" });
    await userEvent.type(within(edit).getByLabelText("Description"), "Updated policy");
    await userEvent.click(within(edit).getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path, options]) => path === "/admin/v1/keys/vk_alpha" && options?.method === "PUT")).toBe(true));
    await userEvent.click(screen.getByRole("button", { name: "Disable" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path]) => path === "/admin/v1/keys/vk_alpha/disable")).toBe(true));
    await userEvent.click(screen.getByRole("button", { name: "Rotate" }));
    const rotated = await screen.findByRole("dialog", { name: "Virtual key created" });
    expect(within(rotated).getByDisplayValue("sk-ag-rotated-once")).toBeInTheDocument();
    await userEvent.click(within(rotated).getByRole("button", { name: "Close" }));
    await userEvent.click(screen.getByRole("button", { name: "Revoke" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path, options]) => path === "/admin/v1/keys/vk_alpha" && options?.method === "DELETE")).toBe(true));
  });
});
