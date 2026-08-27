import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { CustomerInsightsPage } from "./CustomerInsightsPage";
import { EndpointPage } from "./EndpointPage";
import { PlaygroundPage } from "./PlaygroundPage";
import { UsagePage } from "./UsagePage";

function authenticated(node: React.ReactNode) {
  sessionStorage.setItem("ai-gateway.admin-token", "token");
  return render(<AuthProvider>{node}</AuthProvider>);
}

describe("operational pages", () => {
  it("renders usage totals and changes the bounded window", async () => {
    const response = { totals: { requests: 4, total_tokens: 20, spend: "USD 0.10", average_latency_ms: 12 }, by_model: [{ model: "gpt", requests: 4, tokens: 20 }], by_provider: [] };
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify(response), { status: 200 }));
    authenticated(<UsagePage />);
    expect(await screen.findByText("USD 0.10")).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Window"), "7");
    await waitFor(() => expect(fetchMock).toHaveBeenLastCalledWith("/admin/v1/usage/report?days=7", expect.anything()));
  });

  it("runs a playground request with max_completion_tokens", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ choices: [{ message: { content: "OK" } }] }), { status: 200 }));
    authenticated(<PlaygroundPage />);
    await userEvent.type(screen.getByLabelText("Model"), "gpt");
    await userEvent.type(screen.getByLabelText("Message"), "hello");
    await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    expect(await screen.findByText("OK")).toBeInTheDocument();
    expect(String(fetchMock.mock.calls[0][1]?.body)).toContain('"max_completion_tokens":256');
  });

  it("queries customer usage by encoded scope", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ requests: 3 }), { status: 200 }));
    authenticated(<CustomerInsightsPage />);
    await userEvent.selectOptions(screen.getByLabelText("Scope"), "user");
    await userEvent.type(screen.getByLabelText("Scope ID"), "user@example.test");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(await screen.findByText(/"requests": 3/)).toBeInTheDocument();
    expect(fetchMock.mock.calls[0][0]).toBe("/admin/v1/customers/user/user%40example.test/usage?days=30");
  });

  it("renders diagnostic endpoint JSON", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ strategy: "adaptive" }), { status: 200 }));
    authenticated(<EndpointPage eyebrow="Reliability" title="Routing" description="Diagnostics" path="/admin/v1/routing/diagnostics" />);
    expect(await screen.findByText(/"strategy": "adaptive"/)).toBeInTheDocument();
  });
});
