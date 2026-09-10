import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { ModelOnboardingPage } from "./ModelOnboardingPage";

function json(value: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } }));
}

describe("ModelOnboardingPage", () => {
  it("discovers models, validates a plan and applies one atomic configuration", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const path = String(input);
      if (!options?.method && path === "/admin/v1/providers") return json({ data: [{ id: "azure", type: "openai-compatible", base_url: "https://azure.example/v1", enabled: true }] });
      if (!options?.method && path === "/admin/v1/credentials") return json({ data: [{ id: "azure-key", provider_id: "azure", description: "Production" }] });
      if (!options?.method && path === "/admin/v1/model-catalog") return json({ version: "v1", models: [] });
      if (!options?.method && path === "/admin/v1/model-groups") return json({ data: [] });
      if (path.endsWith("/test")) return json({ provider_id: "azure", status: "available", latency_ms: 12, model_count: 2 });
      if (path.endsWith("/discover-models")) return json({ data: [{ id: "gpt-a" }, { id: "gpt-b" }] });
      if (path === "/admin/v1/model-onboarding/plan" && options?.method === "POST") {
        const body = JSON.parse(String(options.body));
        return json({ revision: 7, catalog_version: body.catalog.version, deployments: body.deployments, model_groups: body.model_groups, changes: ["replace catalog", "create deployment", "upsert group"] });
      }
      if (path === "/admin/v1/model-onboarding/apply" && options?.method === "POST") {
        const body = JSON.parse(String(options.body));
        return json({ revision: 8, catalog_version: body.catalog.version, deployments: body.deployments, model_groups: body.model_groups, changes: ["replace catalog", "create deployment", "upsert group"] });
      }
      return json({ error: { message: `Unexpected ${path}` } }, 500);
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<MemoryRouter initialEntries={["/model-onboarding?provider_id=azure&credential_id=azure-key"]}><AuthProvider><ModelOnboardingPage /></AuthProvider></MemoryRouter>);

    await screen.findByRole("option", { name: "azure — openai-compatible" });
    expect(screen.getByLabelText("Credential")).toHaveValue("azure-key");
    await userEvent.click(screen.getByRole("button", { name: "Test & discover models" }));
    expect(await screen.findByText("gpt-a")).toBeInTheDocument();
    await userEvent.click(screen.getAllByRole("checkbox")[0]);
    await userEvent.click(screen.getByRole("button", { name: "Review 1 model(s)" }));
    await userEvent.click(screen.getByLabelText("Capabilities gpt-a"));
    await userEvent.click(screen.getByRole("option", { name: /Tools/ }));
    await userEvent.type(screen.getByLabelText("Input cost gpt-a"), "0.2");
    await userEvent.type(screen.getByLabelText("Output cost gpt-a"), "2");
    await userEvent.click(screen.getByRole("button", { name: "Apply configuration" }));

    expect(await screen.findByText("Onboarding complete")).toBeInTheDocument();
    const calls = fetchMock.mock.calls.map(([path, options]) => ({ path: String(path), method: options?.method, body: options?.body ? JSON.parse(String(options.body)) : undefined }));
    const applyCall = calls.find((call) => call.path === "/admin/v1/model-onboarding/apply" && call.method === "POST");
    expect(applyCall?.body).toMatchObject({
      expected_revision: 7,
      deployments: [{ id: "azure-gpt-a", provider_id: "azure", credential_id: "azure-key", upstream_model: "gpt-a", models: ["gpt-a"], capabilities: ["chat", "stream", "tools"] }],
      model_groups: [{ id: "gpt-a", deployment_ids: ["azure-gpt-a"] }]
    });
    expect(applyCall?.body.catalog.models[0]).toMatchObject({ provider: "azure", model: "gpt-a", capabilities: ["chat", "stream", "tools"], input_cost_per_1m: 0.2, output_cost_per_1m: 2, currency: "USD" });
    expect(calls.filter((call) => call.path === "/admin/v1/model-onboarding/plan")).toHaveLength(2);
    expect(calls.some((call) => call.method === "POST" && (call.path === "/admin/v1/model-deployments" || call.path === "/admin/v1/model-groups") || call.method === "PUT" && call.path === "/admin/v1/model-catalog")).toBe(false);
    await waitFor(() => expect(fetchMock.mock.calls.some(([path]) => String(path).endsWith("/discover-models"))).toBe(true));
  });
  it("offers native provider settings when creating a provider", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      if (String(input) === "/admin/v1/model-catalog") return json({ version: "v1", models: [] });
      if (String(input) === "/admin/v1/provider-capabilities") return json({ data: [
        { type: "azure-openai", operations: ["chat"], auth_types: ["api_key", "entra"] },
        { type: "gemini", operations: ["chat"], auth_types: ["api_key", "gcp_adc"] },
        { type: "bedrock", operations: ["chat"], auth_types: ["bearer", "aws_sigv4"] }
      ] });
      return json({ data: [] });
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<MemoryRouter><AuthProvider><ModelOnboardingPage /></AuthProvider></MemoryRouter>);
    await screen.findByRole("option", { name: "+ Create provider" });
    await userEvent.selectOptions(screen.getByLabelText("Provider"), "__new");
    expect(screen.getByRole("option", { name: "cohere" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "mistral" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "openrouter" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "voyage" })).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Provider type"), "gemini");
    expect(screen.getByLabelText("Provider type")).toHaveValue("gemini");
    expect(screen.queryByLabelText("Azure API version")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Google authentication")).toHaveValue("api_key");
    expect(screen.queryByRole("option", { name: "entra" })).not.toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "aws_sigv4" })).not.toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Google authentication"), "gcp_adc");
    expect(screen.getByLabelText("Google authentication")).toHaveValue("gcp_adc");
    await userEvent.selectOptions(screen.getByLabelText("Provider type"), "azure-openai");
    expect(screen.getByLabelText("Azure API version")).toBeInTheDocument();
    expect(screen.getByLabelText("Azure authentication")).toHaveValue("api_key");
    await userEvent.selectOptions(screen.getByLabelText("Provider type"), "bedrock");
    expect(screen.getByLabelText("Bedrock authentication")).toHaveValue("bearer");
    expect(screen.getByLabelText("AWS region")).toBeInTheDocument();
  });

  it("preserves Gemini workload authentication when creating a provider", async () => {
    let providerBody: Record<string, unknown> | undefined;
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const path = String(input);
      if (!options?.method && path === "/admin/v1/model-catalog") return json({ version: "v1", models: [] });
      if (!options?.method) return json({ data: [] });
      if (path === "/admin/v1/providers" && options.method === "POST") {
        providerBody = JSON.parse(String(options.body));
        return json({ ...providerBody, enabled: true });
      }
      if (path.endsWith("/test")) return json({ provider_id: "google", status: "available", latency_ms: 1, model_count: 0 });
      if (path.endsWith("/discover-models")) return json({ data: [] });
      return json({ error: { message: `Unexpected ${path}` } }, 500);
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<MemoryRouter><AuthProvider><ModelOnboardingPage /></AuthProvider></MemoryRouter>);

    await screen.findByRole("option", { name: "+ Create provider" });
    await userEvent.selectOptions(screen.getByLabelText("Provider"), "__new");
    await userEvent.type(screen.getByLabelText("Provider ID"), "google");
    await userEvent.selectOptions(screen.getByLabelText("Provider type"), "gemini");
    await userEvent.type(screen.getByLabelText("Base URL"), "https://generativelanguage.googleapis.com");
    await userEvent.selectOptions(screen.getByLabelText("Google authentication"), "gcp_adc");
    await userEvent.click(screen.getByRole("button", { name: "Test & discover models" }));

    await waitFor(() => expect(providerBody).toBeDefined());
    expect(providerBody).toMatchObject({ id: "google", type: "gemini", auth_type: "gcp_adc" });
  });

  it("preserves Bedrock signing settings when creating a provider", async () => {
    let providerBody: Record<string, unknown> | undefined;
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const path = String(input);
      if (!options?.method && path === "/admin/v1/model-catalog") return json({ version: "v1", models: [] });
      if (!options?.method) return json({ data: [] });
      if (path === "/admin/v1/providers" && options.method === "POST") {
        providerBody = JSON.parse(String(options.body));
        return json({ ...providerBody, enabled: true });
      }
      if (path.endsWith("/test")) return json({ provider_id: "aws", status: "available", latency_ms: 1, model_count: 0 });
      if (path.endsWith("/discover-models")) return json({ data: [] });
      return json({ error: { message: `Unexpected ${path}` } }, 500);
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<MemoryRouter><AuthProvider><ModelOnboardingPage /></AuthProvider></MemoryRouter>);

    await screen.findByRole("option", { name: "+ Create provider" });
    await userEvent.selectOptions(screen.getByLabelText("Provider"), "__new");
    await userEvent.type(screen.getByLabelText("Provider ID"), "aws");
    await userEvent.selectOptions(screen.getByLabelText("Provider type"), "bedrock");
    await userEvent.type(screen.getByLabelText("Base URL"), "https://bedrock-runtime.us-east-1.amazonaws.com");
    await userEvent.selectOptions(screen.getByLabelText("Bedrock authentication"), "aws_sigv4");
    await userEvent.type(screen.getByLabelText("AWS region"), "us-east-1");
    await userEvent.click(screen.getByRole("button", { name: "Test & discover models" }));

    await waitFor(() => expect(providerBody).toBeDefined());
    expect(providerBody).toMatchObject({ id: "aws", type: "bedrock", auth_type: "aws_sigv4", region: "us-east-1" });
  });

  it("uses embedding capabilities when onboarding a Voyage model", async () => {
    const calls: Array<{ path: string; body?: unknown }> = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const path = String(input);
      const body = options?.body ? JSON.parse(String(options.body)) as { deployments?: unknown; model_groups?: unknown } : undefined;
      calls.push({ path, body });
      if (!options?.method && path === "/admin/v1/providers") return json({ data: [{ id: "voyage", type: "voyage", base_url: "https://provider.example", enabled: true }] });
      if (!options?.method && path === "/admin/v1/credentials") return json({ data: [] });
      if (!options?.method && path === "/admin/v1/model-catalog") return json({ version: "v1", models: [] });
      if (!options?.method && path === "/admin/v1/model-groups") return json({ data: [] });
      if (!options?.method && path === "/admin/v1/provider-capabilities") return json({ data: [{ type: "voyage", operations: ["embeddings", "rerank"], capabilities: ["embeddings", "rerank"] }] });
      if (path.endsWith("/test")) return json({ provider_id: "voyage", status: "available", latency_ms: 1, model_count: 1 });
      if (path.endsWith("/discover-models")) return json({ data: [{ id: "voyage-4" }] });
      if (path === "/admin/v1/model-onboarding/plan") return json({ revision: 1, catalog_version: "v1", deployments: body?.deployments, model_groups: body?.model_groups, changes: [] });
      return json({ error: { message: `Unexpected ${path}` } }, 500);
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<MemoryRouter><AuthProvider><ModelOnboardingPage /></AuthProvider></MemoryRouter>);
    await screen.findByRole("option", { name: "voyage — voyage" });
    await userEvent.click(screen.getByRole("button", { name: "Test & discover models" }));
    await userEvent.click((await screen.findAllByRole("checkbox"))[0]);
    await userEvent.click(screen.getByRole("button", { name: "Review 1 model(s)" }));
    await screen.findByText("Review onboarding plan");
    await userEvent.click(screen.getByLabelText("Capabilities voyage-4"));
    expect(screen.getByRole("option", { name: /Rerank/ })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /Image generation/ })).not.toBeInTheDocument();
    const plan = calls.find((call) => call.path === "/admin/v1/model-onboarding/plan")?.body as { deployments?: Array<{ capabilities?: string[] }> };
    expect(plan.deployments?.[0].capabilities).toEqual(["embeddings"]);
  });

});
