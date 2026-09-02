import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { GuardrailsPage } from "./GuardrailsPage";

const json = (payload: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } }));

const policies = [
  { name: "strict", description: "DLP and antivirus", dlp: true, av: true, enabled: true },
  { name: "av-only", description: "Malware scan", dlp: false, av: true, enabled: true },
  { name: "scanner-down", description: "Unavailable scanner fixture", dlp: true, av: false, enabled: true },
  { name: "disabled", description: "Not active", dlp: true, av: false, enabled: false },
];

function renderPage() {
  sessionStorage.setItem("ai-gateway.admin-token", "guardrail-token");
  return render(<MemoryRouter initialEntries={["/guardrails"]}><AuthProvider><GuardrailsPage /></AuthProvider></MemoryRouter>);
}

describe("GuardrailsPage", () => {
  afterEach(() => { vi.restoreAllMocks(); sessionStorage.clear(); });

  it("joins policy coverage and creates a gateway-native guardrail policy", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === "/admin/v1/guardrail-policies") return json({ data: policies });
      if (url === "/admin/v1/policy-attachments") return json({ data: [{ id: "global-strict", policy_name: "strict", scope: "*" }] });
      if (url === "/admin/v1/model-deployments") return json({ data: [{ id: "azure-primary", guardrail_policy: "strict", enabled: true, runtime_state: "available" }] });
      if (url === "/admin/v1/guardrail-policies/pii-baseline" && init?.method === "PUT") return json({ name: "pii-baseline", ...JSON.parse(String(init.body)) });
      return json({ error: { message: `unexpected ${init?.method || "GET"} ${url}` } }, 500);
    });
    renderPage();
    const strict = (await screen.findByText("strict")).closest("tr")!;
    expect(within(strict).getAllByText("1", { selector: "td" })).toHaveLength(2);
    expect(within(strict).getByText("DLP")).toBeInTheDocument();
    expect(within(strict).getByText("Antivirus")).toBeInTheDocument();
    await userEvent.click(within(strict).getByRole("button", { name: "Actions for guardrail policy strict" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Inspect" }));
    expect(await screen.findByRole("dialog", { name: "Guardrail policy strict" })).toHaveTextContent("azure-primary");
    expect(screen.getByRole("dialog", { name: "Guardrail policy strict" })).toHaveTextContent("global-strict");
    await userEvent.click(screen.getByRole("button", { name: "Close policy details" }));
    await userEvent.click(screen.getByRole("button", { name: "Create Guardrail Policy" }));
    await userEvent.type(screen.getByLabelText("Policy name"), "pii-baseline");
    await userEvent.type(screen.getByLabelText("Description"), "PII baseline");
    await userEvent.click(screen.getByLabelText("Run DLP scanner"));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Select at least one scanner");
    await userEvent.click(screen.getByLabelText("Run DLP scanner"));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, init]) => String(url) === "/admin/v1/guardrail-policies/pii-baseline" && init?.method === "PUT")).toBe(true));
    const call = fetchMock.mock.calls.find(([url, init]) => String(url) === "/admin/v1/guardrail-policies/pii-baseline" && init?.method === "PUT")!;
    expect(JSON.parse(String(call[1]?.body))).toEqual({ description: "PII baseline", dlp: true, av: false, enabled: true });
  });

  it("compares multiple enabled policies and renders only metadata-safe outcomes", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === "/admin/v1/guardrail-policies") return json({ data: policies });
      if (url === "/admin/v1/policy-attachments") return json({ data: [] });
      if (url === "/admin/v1/model-deployments") return json({ data: [] });
      if (url === "/admin/v1/compliance/check" && init?.method === "POST") {
        const body = JSON.parse(String(init.body));
        if (body.policy === "scanner-down") return json({ request_id: "request-scanner-down", policy: body.policy, allowed: true, checks: { dlp: "unavailable" }, content_stored: false }, 503);
        return json({ request_id: `request-${body.policy}`, policy: body.policy, allowed: body.policy !== "av-only", checks: body.policy === "strict" ? { dlp: "passed", av: "passed" } : { dlp: "disabled", av: "rejected" }, content_stored: false });
      }
      return json({ error: { message: `unexpected ${init?.method || "GET"} ${url}` } }, 500);
    });
    renderPage();
    await screen.findByText("strict");
    await userEvent.click(screen.getByRole("button", { name: "Test Guardrails" }));
    const selector = screen.getByRole("combobox", { name: "Policies" });
    await userEvent.type(selector, "strict{enter}");
    expect(screen.getByRole("button", { name: "Remove policy strict" })).toBeInTheDocument();
    await userEvent.type(selector, "av-only{enter}");
    await userEvent.type(selector, "scanner-down{enter}");
    const projection = "sensitive fixture that must not be returned by scanners";
    await userEvent.type(screen.getByLabelText("Text projection"), projection);
    await userEvent.click(screen.getByRole("button", { name: "Test 3 policies" }));
    const results = await screen.findByLabelText("Guardrail test results");
    expect(within(results).getByText("Allowed")).toBeInTheDocument();
    expect(within(results).getByText("Rejected")).toBeInTheDocument();
    expect(within(results).getByText("Unavailable")).toBeInTheDocument();
    expect(within(results).getByText("request-strict")).toBeInTheDocument();
    expect(within(results).getByText("request-av-only")).toBeInTheDocument();
    expect(within(results).getAllByText("No")).toHaveLength(2);
    expect(within(results).queryByText(projection)).not.toBeInTheDocument();
    const checks = fetchMock.mock.calls.filter(([url, init]) => String(url) === "/admin/v1/compliance/check" && init?.method === "POST");
    expect(checks.map(([, init]) => JSON.parse(String(init?.body)))).toEqual([{ policy: "strict", text: projection }, { policy: "av-only", text: projection }, { policy: "scanner-down", text: projection }]);
    expect(screen.getByText(/raw scanner responses are not retained or returned/i)).toBeInTheDocument();
  });
});
