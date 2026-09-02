import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { GuardrailMonitorPage } from "./GuardrailMonitorPage";

const json = (payload: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } }));
const summary = { total: 10, passed: 7, rejected: 2, unavailable: 1, duration_ms: 250, average_duration_ms: 25 };
const report = {
  retention: 1000, retained_events: 10, retention_full: false, scope: "shared_redis", store_available: true, store_errors: 0,
  started_at: "2026-09-02T09:00:00Z", oldest_retained_at: "2026-09-02T09:30:00Z", filters: { window: "retained" },
  summary, by_module: { dlp: { ...summary, total: 6 }, av: { ...summary, total: 4 } }, filtered_summary: summary,
  filtered_by_module: { dlp: { total: 6, passed: 4, rejected: 1, unavailable: 1, duration_ms: 180, average_duration_ms: 30 }, av: { total: 4, passed: 3, rejected: 1, unavailable: 0, duration_ms: 70, average_duration_ms: 17.5 } },
  by_policy: { strict: summary }, timeline: [{ started_at: "2026-09-02T09:30:00Z", summary }, { started_at: "2026-09-02T10:30:00Z", summary: { ...summary, total: 3, rejected: 1 } }],
  events: [{ occurred_at: "2026-09-02T10:31:00Z", request_id: "req-safe", policy: "strict", module: "dlp", source: "inference", outcome: "rejected", duration_ms: 34 }], content_stored: false
};
const policies = { data: [{ name: "strict", description: "Strict checks", dlp: true, av: true, enabled: true }] };

function renderPage(path = "/guardrails-monitor") {
  sessionStorage.setItem("ai-gateway.admin-token", "token");
  return render(<MemoryRouter initialEntries={[path]}><AuthProvider><Routes><Route path="/guardrails-monitor" element={<GuardrailMonitorPage />} /><Route path="/guardrails-monitor/:module" element={<GuardrailMonitorPage />} /></Routes></AuthProvider></MemoryRouter>);
}

describe("GuardrailMonitorPage", () => {
  afterEach(() => { vi.restoreAllMocks(); sessionStorage.clear(); });

  it("renders shared performance, timeline and metadata-only events", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      if (url === "/admin/v1/session") return json({ roles: ["admin"], capabilities: ["admin"] });
      if (url === "/admin/v1/guardrail-policies") return json(policies);
      return json(report);
    });
    renderPage();
    expect(await screen.findByRole("heading", { name: "Guardrail monitor" })).toBeInTheDocument();
    expect(screen.getByText("Shared Redis history across gateway replicas")).toBeInTheDocument();
    expect(within(screen.getByText("Evaluations").closest("article")!).getByText("10")).toBeInTheDocument();
    expect(within(screen.getByText("Pass rate").closest("article")!).getByText("70.0%")).toBeInTheDocument();
    expect(screen.getByLabelText("Guardrail evaluation timeline")).toBeInTheDocument();
    expect(screen.getByText("req-safe")).toBeInTheDocument();
    expect(screen.getAllByText("DLP").length).toBeGreaterThan(0);
    expect(screen.getByText("AV")).toBeInTheDocument();
    expect(screen.getByText(/Prompts, responses and scanner details are not stored/)).toBeInTheDocument();
    expect(screen.queryByText(/prompt text/i)).not.toBeInTheDocument();
  });

  it("applies server-side window, policy, source and outcome filters", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      if (url === "/admin/v1/session") return json({ roles: ["admin"], capabilities: ["admin"] });
      if (url === "/admin/v1/guardrail-policies") return json(policies);
      return json(report);
    });
    renderPage();
    await screen.findByText("Shared Redis history across gateway replicas");
    await userEvent.selectOptions(screen.getByLabelText("Guardrail window"), "1h");
    await userEvent.selectOptions(screen.getByLabelText("Guardrail policy filter"), "strict");
    await userEvent.selectOptions(screen.getByLabelText("Guardrail source filter"), "compliance");
    await userEvent.selectOptions(screen.getByLabelText("Guardrail outcome filter"), "unavailable");
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => {
      const value = String(url);
      return value.includes("/admin/v1/guardrails/monitor?") && value.includes("window=1h") && value.includes("policy=strict") && value.includes("source=compliance") && value.includes("outcome=unavailable");
    })).toBe(true));
  });

  it("hydrates a shareable policy filter from the route query", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      if (url === "/admin/v1/guardrail-policies") return json(policies);
      return json(report);
    });
    renderPage("/guardrails-monitor?policy=strict&window=1h&source=compliance");
    expect(await screen.findByDisplayValue("strict")).toBeInTheDocument();
    expect(screen.getByDisplayValue("Last hour")).toBeInTheDocument();
    expect(screen.getByDisplayValue("Compliance playground")).toBeInTheDocument();
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => {
      const value = String(url);
      return value.includes("/admin/v1/guardrails/monitor?") && value.includes("window=1h") && value.includes("policy=strict") && value.includes("source=compliance");
    })).toBe(true));
  });

  it("opens a route-based module drill-down with policy impact", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      if (url === "/admin/v1/session") return json({ roles: ["admin"], capabilities: ["admin"] });
      if (url === "/admin/v1/guardrail-policies") return json(policies);
      return json(report);
    });
    renderPage();
    await screen.findByText("Shared Redis history across gateway replicas");
    await userEvent.click(screen.getByRole("button", { name: "Actions for guardrail dlp" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Inspect" }));
    expect(await screen.findByRole("heading", { name: "DLP guardrail" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Policy breakdown" })).toBeInTheDocument();
    expect(screen.getAllByText("strict").length).toBeGreaterThan(0);
    expect(screen.getByRole("button", { name: "Back to overview" })).toBeInTheDocument();
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => String(url).includes("module=dlp"))).toBe(true));
  });
});
