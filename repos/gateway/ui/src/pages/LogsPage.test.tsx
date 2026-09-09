import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { LogsPage } from "./LogsPage";

const json = (payload: unknown) => new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } });

describe("LogsPage", () => {
  it("combines request and audit logs in isolated tabs", async () => {
    const requested: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const path = String(input);
      requested.push(path);
      if (path.includes("request-logs/settings")) return json({ content_stored: false });
      if (path.includes("request-logs")) return json({ data: [{ request_id: "req-1", timestamp: "2026-08-28T10:00:00Z", status: "ok", model: "gpt", provider_id: "azure", cache_status: "hit", cache_kind: "semantic", total_tokens: 10, cache_read_input_tokens: 8, cache_write_input_tokens: 2, search_requests: 2, search_requests_estimated: false, usage_estimated: true, latency_ms: 120, first_token_latency_ms: 35, retry_count: 2, fallback_count: 1, cost: 0.1, currency: "USD" }] });
      if (path.includes("organizations")) return json({ data: [{ id: "org-1", name: "Platform" }] });
      if (path.includes("teams")) return json({ data: [{ id: "team-1", name: "Core" }] });
      if (path.includes("users")) return json({ data: [{ id: "user-1", email: "operator@example.com" }] });
      if (path.includes("audit/events")) return json({ data: [{ id: 7, occurred_at: "2026-08-28T11:00:00Z", action: "provider.update", target_type: "provider", target_id: "azure", actor_id: "admin", outcome: "succeeded", details: { changed: true } }] });
      return json({ data: [] });
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token"); render(<MemoryRouter initialEntries={["/logs"]}><AuthProvider><LogsPage /></AuthProvider></MemoryRouter>);
    expect(await screen.findByText("req-1")).toBeInTheDocument();
    expect(screen.getByText("Estimated")).toBeInTheDocument();
    expect(screen.getByText("semantic")).toBeInTheDocument();
    expect(screen.getByText("35 ms")).toBeInTheDocument();
    expect(screen.getByText("Cache read tokens")).toBeInTheDocument();
    expect(screen.getByText("Cache write tokens")).toBeInTheDocument();
    expect(screen.getByText("Searches")).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Request log window"), "30");
    await userEvent.click(screen.getByRole("button", { name: "Apply window" }));
    await waitFor(() => expect(requested.some((path) => path.includes("request-logs?") && path.includes("days=30"))).toBe(true));
    await userEvent.click(screen.getByRole("button", { name: "Filter" }));
    await userEvent.selectOptions(screen.getByLabelText("Organization"), "org-1");
    await userEvent.selectOptions(screen.getByLabelText("Cache"), "hit");
    await userEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(requested.some((path) => path.includes("organization_id=org-1") && path.includes("cache_status=hit"))).toBe(true));
    expect(screen.getByRole("tab", { name: "Request Logs" })).toHaveAttribute("aria-selected", "true");
    await userEvent.click(screen.getByRole("tab", { name: "Audit Logs" }));
    expect(await screen.findByText("provider.update")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Actions for audit event 7" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Details" }));
    const details = await screen.findByRole("dialog", { name: "Audit log details" });
    expect(within(details).getByText(/changed/)).toBeInTheDocument();
  });

  it("restores a shareable request-log view and opens request details from the URL", async () => {
    const requested: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const path = String(input);
      requested.push(path);
      if (path.includes("request-logs/settings")) return json({ content_stored: false });
      if (path.includes("request-logs/req-deep")) return json({ request_id: "req-deep", status: "error", failure_class: "upstream" });
      if (path.includes("request-logs/groups")) return json({ data: [{ group_id: "session-1", requests: 2, errors: 1, models: ["gpt"], providers: ["azure"], total_tokens: 20, cache_read_input_tokens: 8, cache_write_input_tokens: 2, search_requests: 2, cache_hits: 1, latency_ms: 100, cost: 0.2, currency: "USD", started_at: "2026-08-28T10:00:00Z", ended_at: "2026-08-28T11:00:00Z" }] });
      return json({ data: [] });
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<MemoryRouter initialEntries={["/logs?view=sessions&from=2026-08-01T00%3A00%3A00Z&to=2026-08-02T00%3A00%3A00Z&team_id=team-1&log=req-deep"]}><AuthProvider><LogsPage /></AuthProvider></MemoryRouter>);

    expect(await screen.findByRole("tab", { name: "Sessions" })).toHaveAttribute("aria-selected", "true");
    expect(await screen.findByText("session-1")).toBeInTheDocument();
    expect(await screen.findByRole("dialog", { name: "Request details" })).toHaveTextContent("req-deep");
    expect(screen.getByLabelText("Request log window")).toHaveValue("custom");
    expect(requested.some((path) => path.includes("request-logs/groups?") && path.includes("dimension=session") && path.includes("from=2026-08-01T00%3A00%3A00Z") && path.includes("team_id=team-1"))).toBe(true);
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Request details" })).not.toBeInTheDocument());
  });
});
