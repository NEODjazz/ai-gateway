import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { ModelOnboardingPage } from "./ModelOnboardingPage";

function json(value: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } }));
}

describe("ModelOnboardingPage", () => {
  it("discovers selected provider models and creates catalog, deployment and group records", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const path = String(input);
      if (!options?.method && path === "/admin/v1/providers") return json({ data: [{ id: "azure", type: "openai-compatible", base_url: "https://azure.example/v1", enabled: true }] });
      if (!options?.method && path === "/admin/v1/credentials") return json({ data: [{ id: "azure-key", provider_id: "azure", description: "Production" }] });
      if (!options?.method && path === "/admin/v1/model-catalog") return json({ version: "v1", models: [] });
      if (!options?.method && path === "/admin/v1/model-groups") return json({ data: [] });
      if (path.endsWith("/test")) return json({ provider_id: "azure", status: "available", latency_ms: 12, model_count: 2 });
      if (path.endsWith("/discover-models")) return json({ data: [{ id: "gpt-a" }, { id: "gpt-b" }] });
      if (path === "/admin/v1/model-catalog" && options?.method === "PUT") return json(JSON.parse(String(options.body)));
      if (path === "/admin/v1/model-deployments" && options?.method === "POST") return json(JSON.parse(String(options.body)), 201);
      if (path === "/admin/v1/model-groups" && options?.method === "POST") return json(JSON.parse(String(options.body)), 201);
      return json({ error: { message: `Unexpected ${path}` } }, 500);
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<AuthProvider><ModelOnboardingPage /></AuthProvider>);

    await screen.findByRole("option", { name: "azure — openai-compatible" });
    await userEvent.selectOptions(screen.getByLabelText("Credential"), "azure-key");
    await userEvent.click(screen.getByRole("button", { name: "Test & discover models" }));
    expect(await screen.findByText("gpt-a")).toBeInTheDocument();
    await userEvent.click(screen.getAllByRole("checkbox")[0]);
    await userEvent.click(screen.getByRole("button", { name: "Review 1 model(s)" }));
    await userEvent.type(screen.getByLabelText("Input cost gpt-a"), "0.2");
    await userEvent.type(screen.getByLabelText("Output cost gpt-a"), "2");
    await userEvent.click(screen.getByRole("button", { name: "Apply configuration" }));

    expect(await screen.findByText("Onboarding complete")).toBeInTheDocument();
    const calls = fetchMock.mock.calls.map(([path, options]) => ({ path: String(path), method: options?.method, body: options?.body ? JSON.parse(String(options.body)) : undefined }));
    expect(calls.find((call) => call.path === "/admin/v1/model-deployments" && call.method === "POST")?.body).toMatchObject({ id: "azure-gpt-a", provider_id: "azure", credential_id: "azure-key", upstream_model: "gpt-a", models: ["gpt-a"] });
    expect(calls.find((call) => call.path === "/admin/v1/model-groups" && call.method === "POST")?.body).toMatchObject({ id: "gpt-a", deployment_ids: ["azure-gpt-a"] });
    const catalogCall = calls.find((call) => call.path === "/admin/v1/model-catalog" && call.method === "PUT");
    expect(catalogCall?.body.models[0]).toMatchObject({ provider: "azure", model: "gpt-a", input_cost_per_1m: 0.2, output_cost_per_1m: 2, currency: "USD" });
    await waitFor(() => expect(fetchMock.mock.calls.some(([path]) => String(path).endsWith("/discover-models"))).toBe(true));
  });
});
