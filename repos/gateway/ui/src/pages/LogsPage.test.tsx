import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { LogsPage } from "./LogsPage";

const json = (payload: unknown) => new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } });

describe("LogsPage", () => {
  it("combines request and audit logs in isolated tabs", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const path = String(input);
      if (path.includes("request-logs/settings")) return json({ content_stored: false });
      if (path.includes("request-logs")) return json({ data: [{ request_id: "req-1", timestamp: "2026-08-28T10:00:00Z", status: "ok", model: "gpt", provider_id: "azure", cache_status: "hit", cache_kind: "semantic", total_tokens: 10, usage_estimated: true, latency_ms: 120, first_token_latency_ms: 35, retry_count: 2, fallback_count: 1, cost: 0.1, currency: "USD" }] });
      if (path.includes("audit/events")) return json({ data: [{ id: 7, occurred_at: "2026-08-28T11:00:00Z", action: "provider.update", target_type: "provider", target_id: "azure", actor_id: "admin", outcome: "succeeded", details: { changed: true } }] });
      return json({ data: [] });
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token"); render(<AuthProvider><LogsPage /></AuthProvider>);
    expect(await screen.findByText("req-1")).toBeInTheDocument();
    expect(screen.getByText("Estimated")).toBeInTheDocument();
    expect(screen.getByText("semantic")).toBeInTheDocument();
    expect(screen.getByText("35 ms")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Request Logs" })).toHaveAttribute("aria-selected", "true");
    await userEvent.click(screen.getByRole("tab", { name: "Audit Logs" }));
    expect(await screen.findByText("provider.update")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Actions for audit event 7" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Details" }));
    const details = await screen.findByRole("dialog", { name: "Audit log details" });
    expect(within(details).getByText(/changed/)).toBeInTheDocument();
  });
});
