import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { BudgetsPage } from "./BudgetsPage";

const json = (payload: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } }));
const policy = { id: 7, scope_type: "team", scope_id: "platform", period: "month", currency: "USD", max_cost: 10, max_tokens: 10000, enabled: true, created_at: "2026-09-01T10:00:00Z", updated_at: "2026-09-01T11:00:00Z" };
const expanded = { data: [policy], summaries: { "7": { policy, window_start: "2026-09-01T00:00:00Z", window_end: "2026-10-01T00:00:00Z", used_cost: 8, remaining_cost: 2, used_tokens: 8000, remaining_tokens: 2000 } } };

function renderPage(entry = "/budgets") {
  sessionStorage.setItem("ai-gateway.admin-token", "token");
  return render(<MemoryRouter initialEntries={[entry]}><AuthProvider><BudgetsPage /></AuthProvider></MemoryRouter>);
}

describe("BudgetsPage", () => {
  afterEach(() => { vi.restoreAllMocks(); sessionStorage.clear(); });

  it("shows currency-isolated live usage, reset state, filters and risk", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      if (url === "/admin/v1/session") return json({ roles: ["admin"], capabilities: ["admin"] });
      if (url === "/admin/v1/budgets?expand=summaries") return json(expanded);
      return json({ data: [] });
    });
    renderPage();
    expect(await screen.findByText("team · platform")).toBeInTheDocument();
    expect(screen.getByText("$8.00 / $10.00")).toBeInTheDocument();
    expect(screen.getByText("8,000 / 10,000")).toBeInTheDocument();
    expect(screen.getAllByLabelText("80% used")).toHaveLength(2);
    expect(screen.getByText("2026-10-01 00:00:00 UTC")).toBeInTheDocument();
    expect(screen.getAllByText("At risk").length).toBeGreaterThan(0);
    await userEvent.selectOptions(screen.getByLabelText("Filter budgets by scope"), "provider");
    expect(screen.queryByText("team · platform")).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => url === "/admin/v1/budgets?expand=summaries")).toBe(true);
  });

  it("creates a team budget from configured targets without sending empty limits", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const url = String(input);
      if (url === "/admin/v1/session") return json({ roles: ["admin"], capabilities: ["admin"] });
      if (url === "/admin/v1/budgets?expand=summaries") return json({ data: [], summaries: {} });
      if (url === "/admin/v1/teams?limit=500") return json({ data: [{ id: "platform", name: "Platform", status: "active" }] });
      if (url === "/admin/v1/budgets" && options?.method === "POST") return json({ ...policy, id: 8 }, 201);
      return json({ data: [] });
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Create Budget" }));
    const form = screen.getByRole("dialog", { name: "Create budget" });
    await userEvent.selectOptions(within(form).getByLabelText("Budget scope type"), "team");
    await userEvent.selectOptions(within(form).getByLabelText("Budget scope"), await within(form).findByRole("option", { name: "platform — Platform" }));
    await userEvent.clear(within(form).getByLabelText("Budget currency"));
    await userEvent.type(within(form).getByLabelText("Budget currency"), "eur");
    await userEvent.type(within(form).getByLabelText("Budget maximum tokens"), "5000");
    await userEvent.click(within(form).getByRole("button", { name: "Create budget" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, options]) => url === "/admin/v1/budgets" && options?.method === "POST")).toBe(true));
    const create = fetchMock.mock.calls.find(([url, options]) => url === "/admin/v1/budgets" && options?.method === "POST");
    expect(JSON.parse(String(create?.[1]?.body))).toEqual({ scope_type: "team", scope_id: "platform", period: "month", currency: "EUR", enabled: true, max_tokens: 5000 });
  });

  it("edits and disables a policy through the actions menu", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const url = String(input);
      if (url === "/admin/v1/session") return json({ roles: ["admin"], capabilities: ["admin"] });
      if (url === "/admin/v1/budgets?expand=summaries") return json(expanded);
      if (url === "/admin/v1/teams?limit=500") return json({ data: [{ id: "platform", name: "Platform", status: "active" }] });
      if (url === "/admin/v1/budgets/7" && options?.method === "PUT") return json(policy);
      if (url === "/admin/v1/budgets/7" && options?.method === "DELETE") return new Response(null, { status: 204 });
      return json({ data: [] });
    });
    renderPage();
    await screen.findByText("team · platform");
    await userEvent.click(screen.getByRole("button", { name: "Actions for budget 7" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Edit" }));
    const form = screen.getByRole("dialog", { name: "Edit budget" });
    await userEvent.clear(within(form).getByLabelText("Budget maximum cost"));
    await userEvent.type(within(form).getByLabelText("Budget maximum cost"), "12");
    await userEvent.click(within(form).getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, options]) => url === "/admin/v1/budgets/7" && options?.method === "PUT")).toBe(true));
    await userEvent.click(screen.getByRole("button", { name: "Actions for budget 7" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Disable" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, options]) => url === "/admin/v1/budgets/7" && options?.method === "DELETE")).toBe(true));
  });

  it("opens an edit form from a budget details deep link", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      if (url === "/admin/v1/session") return json({ roles: ["admin"], capabilities: ["admin"] });
      if (url === "/admin/v1/budgets?expand=summaries") return json(expanded);
      if (url === "/admin/v1/teams?limit=500") return json({ data: [{ id: "platform", name: "Platform", status: "active" }] });
      return json({ data: [] });
    });
    renderPage("/budgets?budget_id=7&edit=1");
    const form = await screen.findByRole("dialog", { name: "Edit budget" });
    expect(within(form).getByLabelText("Budget scope type")).toHaveValue("team");
    expect(within(form).getByLabelText("Budget scope")).toHaveValue("platform");
    expect(within(form).getByLabelText("Budget maximum cost")).toHaveValue(10);
  });
});
