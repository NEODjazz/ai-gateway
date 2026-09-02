import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { BudgetDetailsPage } from "./BudgetDetailsPage";

const json = (payload: unknown, status = 200) => Promise.resolve(new Response(status === 204 ? null : JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } }));
const policy = { id: 7, scope_type: "team", scope_id: "platform", period: "month", currency: "USD", max_cost: 10, max_tokens: 10000, enabled: true, created_at: "2026-09-01T10:00:00Z", updated_at: "2026-09-01T11:00:00Z" };
const summary = { policy, window_start: "2026-09-01T00:00:00Z", window_end: "2026-10-01T00:00:00Z", used_cost: 8, remaining_cost: 2, used_tokens: 8000, remaining_tokens: 2000 };
function LocationProbe() { const location = useLocation(); return <div>Budget route {location.pathname}{location.search}</div>; }
function renderPage() {
  sessionStorage.setItem("ai-gateway.admin-token", "token");
  return render(<MemoryRouter initialEntries={["/budgets/7"]}><AuthProvider><Routes><Route path="/budgets/:id" element={<BudgetDetailsPage />} /><Route path="/budgets" element={<LocationProbe />} /></Routes></AuthProvider></MemoryRouter>);
}

describe("BudgetDetailsPage", () => {
  afterEach(() => { vi.restoreAllMocks(); sessionStorage.clear(); });

  it("shows the authoritative window, currency-isolated usage and scope link", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      if (url === "/admin/v1/budgets/7") return json(policy);
      if (url === "/admin/v1/budgets/7/summary") return json(summary);
      return json({}, 500);
    });
    renderPage();
    expect(await screen.findByRole("heading", { name: "Budget 7" })).toBeInTheDocument();
    expect(screen.getByText("At risk")).toBeInTheDocument();
    expect(screen.getByText("$8.00 / $10.00")).toBeInTheDocument();
    expect(screen.getByText("8,000 / 10,000")).toBeInTheDocument();
    expect(screen.getByLabelText("80% cost used")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "platform" })).toHaveAttribute("href", "/teams/platform");
    await userEvent.click(screen.getByRole("button", { name: "Actions for budget 7" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Edit budget" }));
    expect(await screen.findByText("Budget route /budgets?budget_id=7&edit=1")).toBeInTheDocument();
  });

  it("disables the policy and returns to the budget catalog", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const url = String(input);
      if (url === "/admin/v1/budgets/7" && options?.method === "DELETE") return json({}, 204);
      if (url === "/admin/v1/budgets/7") return json(policy);
      if (url === "/admin/v1/budgets/7/summary") return json(summary);
      return json({}, 500);
    });
    renderPage();
    await screen.findByRole("heading", { name: "Budget 7" });
    await userEvent.click(screen.getByRole("button", { name: "Actions for budget 7" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Disable" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, options]) => url === "/admin/v1/budgets/7" && options?.method === "DELETE")).toBe(true));
    expect(await screen.findByText("Budget route /budgets")).toBeInTheDocument();
  });
});
