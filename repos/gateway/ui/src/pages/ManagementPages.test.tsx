import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { GuardrailsPage } from "./GuardrailsPage";
import { OrganizationsPage, TeamsPage } from "./IdentityAssociationPages";
import { RequestLogsPage } from "./RequestLogsPage";
import { RoutingPage } from "./RoutingPage";
import { ResourcePage } from "../components/ResourcePage";
import { resourceConfigs } from "./resourceConfigs";

function renderAuthenticated(node: React.ReactNode) {
  sessionStorage.setItem("ai-gateway.admin-token", "token");
  render(<AuthProvider>{node}</AuthProvider>);
}
const json = (payload: unknown, status = 200) => new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } });

describe("management pages", () => {
  it("simulates routing without invoking a provider", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => String(input).includes("simulate") ? json({ candidates: ["primary"] }) : json({ strategy: "adaptive" }));
    renderAuthenticated(<RoutingPage />);
    await userEvent.type(screen.getByLabelText("Public model"), "gpt");
    await userEvent.type(screen.getByLabelText("Capabilities"), "chat, tools");
    await userEvent.click(screen.getByRole("button", { name: "Simulate" }));
    expect(await screen.findByText(/"primary"/)).toBeInTheDocument();
    expect(String(fetchMock.mock.calls.find((call) => String(call[0]).includes("simulate"))?.[1]?.body)).toContain('"capabilities":["chat","tools"]');
  });

  it("runs a metadata-only compliance check", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => String(input).includes("compliance/check") ? json({ allowed: true, content_stored: false }) : json({ data: [] }));
    renderAuthenticated(<GuardrailsPage />);
    await userEvent.type(screen.getByLabelText("Policy"), "strict");
    await userEvent.type(screen.getByLabelText("Text projection"), "safe text");
    await userEvent.click(screen.getByRole("button", { name: "Run compliance check" }));
    expect(await screen.findByText(/"content_stored": false/)).toBeInTheDocument();
  });

  it("creates a scoped policy attachment from configured resources", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      if (url === "/admin/v1/policy-attachments") return json({ data: [] });
      if (url === "/admin/v1/guardrail-policies") return json({ data: [{ name: "strict", description: "DLP", enabled: true }, { name: "disabled", description: "Off", enabled: false }] });
      if (url.includes("/admin/v1/teams")) return json({ data: [{ id: "care-a", name: "Care" }] });
      if (url.includes("/admin/v1/keys")) return json({ data: [{ id: "vk-1", alias: "clinical-prod" }] });
      if (url === "/admin/v1/model-catalog") return json({ models: [{ model: "gpt-5.6", provider: "azure" }] });
      if (url === "/admin/v1/policy-attachments/clinical") return json({ id: "clinical", policy_name: "strict" });
      return json({ data: [] });
    });
    renderAuthenticated(<ResourcePage config={resourceConfigs.policyAttachments} />);
    await screen.findByText("No records found.");
    await userEvent.click(screen.getByRole("button", { name: "Create Policy Attachment" }));
    await userEvent.type(screen.getByLabelText("ID"), "clinical");
    expect(screen.queryByRole("option", { name: /disabled/ })).not.toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Guardrail policy"), "strict");
    await userEvent.selectOptions(screen.getByLabelText("Teams"), "care-a");
    await userEvent.selectOptions(screen.getByLabelText("Models"), "gpt-5.6");
    await userEvent.selectOptions(screen.getByLabelText("Scope"), "*");
    expect(screen.queryByLabelText("Teams")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Models")).not.toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Scope"), "specific");
    expect(Array.from((screen.getByLabelText("Teams") as HTMLSelectElement).selectedOptions)).toHaveLength(0);
    expect(Array.from((screen.getByLabelText("Models") as HTMLSelectElement).selectedOptions)).toHaveLength(0);
    await userEvent.selectOptions(screen.getByLabelText("Teams"), "care-a");
    await userEvent.selectOptions(screen.getByLabelText("Models"), "gpt-5.6");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetchMock.mock.calls.some((call) => call[0] === "/admin/v1/policy-attachments/clinical" && call[1]?.method === "PUT")).toBe(true));
    const save = fetchMock.mock.calls.find((call) => call[0] === "/admin/v1/policy-attachments/clinical");
    expect(save?.[1]?.body).toContain('"policy_name":"strict"');
    expect(save?.[1]?.body).toContain('"teams":["care-a"]');
    expect(save?.[1]?.body).toContain('"models":["gpt-5.6"]');
  });

  it("creates a managed tag with model restrictions", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      if (url === "/admin/v1/tags") return json({ data: [] });
      if (url === "/admin/v1/model-catalog") return json({ models: [{ model: "gpt-5.6", provider: "azure" }, { model: "llama3.2:latest", provider: "ollama" }] });
      if (url === "/admin/v1/tags/regulated") return json({ name: "regulated", allowed_models: ["gpt-5.6"], enabled: true });
      return json({ data: [] });
    });
    renderAuthenticated(<ResourcePage config={resourceConfigs.tags} />);
    await screen.findByText("No records found.");
    await userEvent.click(screen.getByRole("button", { name: "Create Tag" }));
    await userEvent.type(screen.getByLabelText("Tag name"), "regulated");
    await userEvent.type(screen.getByLabelText("Description"), "Regulated workloads");
    await userEvent.selectOptions(screen.getByLabelText("Allowed models"), "gpt-5.6");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetchMock.mock.calls.some((call) => call[0] === "/admin/v1/tags/regulated" && call[1]?.method === "PUT")).toBe(true));
    const save = fetchMock.mock.calls.find((call) => call[0] === "/admin/v1/tags/regulated");
    expect(save?.[1]?.body).toContain('"allowed_models":["gpt-5.6"]');
    expect(save?.[1]?.body).toContain('"enabled":true');
  });

  it("creates a tag-scoped budget with the full reset-period set", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      if (String(input) === "/admin/v1/budgets") return json({ data: [] });
      return json({ id: 41, scope_type: "tag", scope_id: "production" });
    });
    renderAuthenticated(<ResourcePage config={resourceConfigs.budgets} />);
    await screen.findByText("No records found.");
    await userEvent.click(screen.getByRole("button", { name: "Add" }));
    await userEvent.selectOptions(screen.getByLabelText("Scope type"), "tag");
    await userEvent.type(screen.getByLabelText("Scope ID"), "production");
    await userEvent.selectOptions(screen.getByLabelText("Reset period"), "hour");
    await userEvent.clear(screen.getByLabelText("Maximum tokens"));
    await userEvent.type(screen.getByLabelText("Maximum tokens"), "1000");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetchMock.mock.calls.some((call) => call[0] === "/admin/v1/budgets" && call[1]?.method === "POST")).toBe(true));
    const save = fetchMock.mock.calls.find((call) => call[0] === "/admin/v1/budgets" && call[1]?.method === "POST");
    expect(save?.[1]?.body).toContain('"scope_type":"tag"');
    expect(save?.[1]?.body).toContain('"scope_id":"production"');
    expect(save?.[1]?.body).toContain('"period":"hour"');
  });

  it("assigns a user to a team with roles", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(json({ data: [] }));
    renderAuthenticated(<TeamsPage />); await screen.findByText("No records found.");
    await userEvent.type(screen.getByLabelText("Team ID"), "team-a");
    await userEvent.type(screen.getByLabelText("User ID"), "user-a");
    await userEvent.type(screen.getByLabelText("Roles"), "operator, viewer");
    await userEvent.click(screen.getByRole("button", { name: "Assign" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(fetchMock.mock.calls[1][0]).toBe("/admin/v1/teams/team-a/members/user-a");
    expect(fetchMock.mock.calls[1][1]?.body).toBe('{"roles":["operator","viewer"]}');
  });

  it("assigns a team to an organization", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(json({ data: [] }));
    renderAuthenticated(<OrganizationsPage />); await screen.findByText("No records found.");
    await userEvent.type(screen.getByLabelText("Organization ID"), "org-a");
    await userEvent.type(screen.getByLabelText("Team ID"), "team-a");
    await userEvent.click(screen.getByRole("button", { name: "Assign" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(fetchMock.mock.calls[1][0]).toBe("/admin/v1/organizations/org-a/teams/team-a");
  });

  it("loads request-log privacy settings and details", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      if (url.includes("settings")) return json({ content_stored: false, retention_days: 730 });
      if (url.endsWith("/req-1")) return json({ request_id: "req-1", upstream_model: "gpt-versioned" });
      if (url.includes("before=")) return json({ data: [{ request_id: "req-2", timestamp: "2026-08-27T14:00:00Z", status: "ok", input_tokens: 1, output_tokens: 2, total_tokens: 3, latency_ms: 10, cost: 0, currency: "USD" }] });
      return json({
        data: [{ request_id: "req-1", session_id: "session-1", trace_id: "0123456789abcdef0123456789abcdef", tags: ["production"], timestamp: "2026-08-27T15:52:34Z", status: "ok", model: "gpt", upstream_model: "gpt-versioned", provider_id: "azure-open-ai", provider_endpoint_name: "luna", provider_endpoint_type: "openai-compatible", cache_status: "miss", input_tokens: 7, output_tokens: 12, total_tokens: 19, latency_ms: 1006, cost: 0.0002414, currency: "USD" }],
        next_before: "2026-08-27T15:52:34Z",
        next_request_id: "req-1"
      });
    });
    renderAuthenticated(<RequestLogsPage />);
    expect(await screen.findByText("req-1")).toBeInTheDocument();
    expect(screen.getByText("2026-08-27 15:52:34 UTC")).toBeInTheDocument();
    expect(screen.getByText("azure-open-ai")).toBeInTheDocument();
    expect(screen.getByText("Miss")).toBeInTheDocument();
    expect(screen.getByText("$0.000241")).toBeInTheDocument();
    expect(screen.getByText("USD")).toBeInTheDocument();
    expect(screen.getByText("1,006 ms")).toBeInTheDocument();
    expect(screen.getByText("0123456789abcdef0123456789abcdef")).toBeInTheDocument();
    expect(screen.getByText("production")).toBeInTheDocument();
    expect(await screen.findByText(/content_stored/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Sessions" }));
    expect(screen.getByText("session-1")).toBeInTheDocument();
    expect(screen.getByText("Aggregated from the loaded request window; spend remains separated by currency.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Traces" }));
    expect(screen.getByText("0123456789abcdef0123456789abcdef")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Requests" }));
    await userEvent.click(screen.getByRole("button", { name: "Load older" }));
    expect(await screen.findByText("req-2")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some((call) => String(call[0]).includes("before=2026-08-27T15%3A52%3A34Z") && String(call[0]).includes("before_request_id=req-1"))).toBe(true);
    await userEvent.click(screen.getByRole("button", { name: "Actions for req-1" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Details" }));
    expect(await screen.findAllByText(/gpt-versioned/)).toHaveLength(2);
  });
});
