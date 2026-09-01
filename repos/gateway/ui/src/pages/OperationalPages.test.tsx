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
    const aggregate = { currency: "USD", requests: 4, errors: 0, input_tokens: 8, output_tokens: 12, total_tokens: 20, cost: 0.00265, avg_latency_ms: 12, cache_hits: 0, cost_per_request: 0.0006625 };
    const response = {
      totals: [aggregate], daily: [{ ...aggregate, date: "2026-08-28" }], by_model: [{ ...aggregate, name: "gpt" }],
      by_provider: [{ ...aggregate, name: "azure-open-ai" }], by_tag: [{ ...aggregate, name: "production" }],
      by_key: [{ ...aggregate, name: "key-1" }], by_user: [{ ...aggregate, name: "user-1" }],
      by_team: [{ ...aggregate, name: "team-1" }], by_organization: [{ ...aggregate, name: "org-1" }]
    };
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify(response), { status: 200 }));
    authenticated(<UsagePage />);
    expect(await screen.findAllByText("$0.00265")).toHaveLength(2);
    expect(screen.getByText("Successful")).toBeInTheDocument();
    expect(screen.getByText("Spend per day")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Models" })); expect(screen.getByText("gpt")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Providers" })); expect(screen.getByText("azure-open-ai")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Tags" })); expect(screen.getByText("production")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Virtual keys" })); expect(screen.getByText("key-1")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Users" })); expect(screen.getByText("user-1")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Teams" })); expect(screen.getByText("team-1")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Organizations" })); expect(screen.getByText("org-1")).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Window"), "7");
    await userEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(fetchMock).toHaveBeenLastCalledWith("/admin/v1/usage/report?days=7", expect.anything()));
  });

  it("keeps spend separated by currency and weights latency by request count", async () => {
    const base = { errors: 0, input_tokens: 0, output_tokens: 0, total_tokens: 10, cache_hits: 0, cost_per_request: 0 };
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({
      totals: [
        { ...base, currency: "USD", requests: 1, cost: 1, avg_latency_ms: 100 },
        { ...base, currency: "EUR", requests: 3, cost: 2, avg_latency_ms: 300 }
      ],
      daily: [],
      by_model: [],
      by_provider: [],
      by_tag: []
    }), { status: 200 }));
    authenticated(<UsagePage />);
    expect(await screen.findByText("$1.00 · €2.00")).toBeInTheDocument();
    expect(screen.getByText("250")).toBeInTheDocument();
    expect(screen.getByText("20")).toBeInTheDocument();
  });

  it("applies LiteLLM-style model, provider and custom date filters", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ totals: [], daily: [], by_model: [], by_provider: [], by_tag: [] }), { status: 200 }));
    authenticated(<UsagePage />); await screen.findByText("Spend per day");
    await userEvent.selectOptions(screen.getByLabelText("Window"), "custom");
    await userEvent.type(screen.getByLabelText("Usage from"), "2026-08-01");
    await userEvent.type(screen.getByLabelText("Usage to"), "2026-08-28");
    await userEvent.type(screen.getByLabelText("Usage model"), "gpt");
    await userEvent.type(screen.getByLabelText("Usage provider"), "azure");
    await userEvent.type(screen.getByLabelText("Usage tag"), "production");
    await userEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(String(fetchMock.mock.calls.at(-1)?.[0])).toContain("model=gpt"));
    expect(String(fetchMock.mock.calls.at(-1)?.[0])).toContain("provider=azure");
    expect(String(fetchMock.mock.calls.at(-1)?.[0])).toContain("tag=production");
    expect(String(fetchMock.mock.calls.at(-1)?.[0])).toContain("from=2026-08-01T00%3A00%3A00.000Z");
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
