import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { ModelOnboardingPage } from "./ModelOnboardingPage";

function json(value: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } }));
}

describe("ModelOnboardingPage", () => {
  it("requires explicit Azure capabilities and prices during Foundry onboarding", async () => {
    const plans: Array<{ deployments: Array<{ capabilities: string[] }> }> = [];
    const applied: Array<{ catalog: { models: Array<{ input_cost_per_1m: number; output_cost_per_1m: number; currency: string }> } }> = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const path = String(input);
      if (!options?.method && path === "/admin/v1/providers") return json({ data: [{ id: "foundry", type: "azure-openai", base_url: "https://example.services.ai.azure.com/api/projects/project-a", auth_type: "entra", enabled: true }] });
      if (!options?.method && path === "/admin/v1/credentials") return json({ data: [] });
      if (!options?.method && path === "/admin/v1/model-catalog") return json({ version: "v1", models: [] });
      if (!options?.method && path === "/admin/v1/model-groups") return json({ data: [] });
      if (!options?.method && path === "/admin/v1/provider-capabilities") return json({ data: [{ type: "azure-openai", capabilities: ["chat", "stream", "embeddings"] }] });
      if (path.endsWith("/test")) return json({ status: "available", latency_ms: 1, model_count: 1 });
      if (path.endsWith("/discover-models")) return json({ data: [{ id: "deploy-a", model_name: "text-embedding-3-large", model_publisher: "Microsoft" }] });
      if (path === "/admin/v1/model-onboarding/plan") {
        const body = JSON.parse(String(options?.body)) as { deployments: Array<{ capabilities: string[] }> };
        plans.push(body);
        return json({ revision: 1, catalog_version: "v1", deployments: body.deployments, model_groups: [], changes: [] });
      }
      if (path === "/admin/v1/model-onboarding/apply") {
        const body = JSON.parse(String(options?.body));
        applied.push(body);
        return json({ revision: 2, catalog_version: "v1", deployments: body.deployments, model_groups: [], changes: [] });
      }
      return json({ error: { message: `Unexpected ${path}` } }, 500);
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<MemoryRouter><AuthProvider><ModelOnboardingPage /></AuthProvider></MemoryRouter>);
    await screen.findByRole("option", { name: "foundry — azure-openai" });
    await userEvent.click(screen.getByRole("button", { name: "Test & discover models" }));
    expect(await screen.findByText("deploy-a — text-embedding-3-large (Microsoft)")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox"));
    await userEvent.click(screen.getByRole("button", { name: "Review 1 model(s)" }));
    await screen.findByText("Review onboarding plan");
    expect(plans[0].deployments[0].capabilities).toEqual([]);
    expect(screen.getByRole("button", { name: "Apply configuration" })).toBeDisabled();
    await userEvent.click(screen.getByLabelText("Capabilities deploy-a"));
    await userEvent.click(screen.getByRole("option", { name: /Embeddings/ }));
    expect(screen.getByRole("button", { name: "Apply configuration" })).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Input cost deploy-a"), "0.2");
    expect(screen.getByRole("button", { name: "Apply configuration" })).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Output cost deploy-a"), "0");
    expect(screen.getByRole("button", { name: "Apply configuration" })).toBeEnabled();
    await userEvent.click(screen.getByRole("button", { name: "Apply configuration" }));
    expect(await screen.findByText("Onboarding complete")).toBeInTheDocument();
    expect(applied[0].catalog.models[0]).toMatchObject({ input_cost_per_1m: 0.2, output_cost_per_1m: 0, currency: "USD" });
  });

  it("uses discovered Ollama capabilities and requires explicit selection when metadata is unavailable", async () => {
    const plans: Array<{ deployments: Array<{ upstream_model: string; capabilities: string[] }> }> = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const path = String(input);
      if (!options?.method && path === "/admin/v1/providers") return json({ data: [{ id: "ollama", type: "ollama", base_url: "http://localhost:11434", enabled: true }] });
      if (!options?.method && path === "/admin/v1/credentials") return json({ data: [] });
      if (!options?.method && path === "/admin/v1/model-catalog") return json({ version: "v1", models: [] });
      if (!options?.method && path === "/admin/v1/model-groups") return json({ data: [] });
      if (!options?.method && path === "/admin/v1/provider-capabilities") return json({ data: [{ type: "ollama", capabilities: ["chat", "responses", "embeddings", "stream"] }] });
      if (path.endsWith("/test")) return json({ status: "available", latency_ms: 1, model_count: 2 });
      if (path.endsWith("/discover-models")) return json({ data: [{ id: "embed", capabilities: ["embeddings"] }, { id: "unknown" }] });
      if (path === "/admin/v1/model-onboarding/plan") {
        const body = JSON.parse(String(options?.body)) as { deployments: Array<{ upstream_model: string; capabilities: string[] }> };
        plans.push(body);
        return json({ revision: 1, catalog_version: "v1", deployments: body.deployments, model_groups: [], changes: [] });
      }
      return json({ error: { message: `Unexpected ${path}` } }, 500);
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<MemoryRouter><AuthProvider><ModelOnboardingPage /></AuthProvider></MemoryRouter>);
    await screen.findByRole("option", { name: "ollama — ollama" });
    await userEvent.click(screen.getByRole("button", { name: "Test & discover models" }));
    expect(await screen.findByText("embed")).toBeInTheDocument();
    await userEvent.click(screen.getAllByRole("checkbox")[0]);
    await userEvent.click(screen.getAllByRole("checkbox")[1]);
    await userEvent.click(screen.getByRole("button", { name: "Review 2 model(s)" }));
    await screen.findByText("Review onboarding plan");
    expect(plans[0].deployments).toEqual(expect.arrayContaining([
      expect.objectContaining({ upstream_model: "embed", capabilities: ["embeddings"] }),
      expect.objectContaining({ upstream_model: "unknown", capabilities: [] })
    ]));
    expect(screen.getByRole("button", { name: "Apply configuration" })).toBeDisabled();
    await userEvent.click(screen.getByLabelText("Capabilities unknown"));
    await userEvent.click(screen.getByRole("option", { name: /Chat/ }));
    expect(screen.getByRole("button", { name: "Apply configuration" })).toBeEnabled();
  });

  it("discovers models, validates a plan and applies one atomic configuration", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const path = String(input);
      if (!options?.method && path === "/admin/v1/providers") return json({ data: [{ id: "azure", type: "openai-compatible", base_url: "https://azure.example/v1", enabled: true }, { id: "vertex", type: "vertex-gemini", base_url: "https://vertex.example/v1/projects/p/locations/l/publishers/google", enabled: true }] });
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
    expect(screen.getByRole("option", { name: "vertex — vertex-gemini" })).toBeInTheDocument();
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
        { type: "bedrock", operations: ["chat"], auth_types: ["bearer", "aws_sigv4"] },
        { type: "vertex-gemini", operations: ["chat", "count_tokens", "stream"], auth_types: ["gcp_adc"] }
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
    expect(screen.getByRole("option", { name: "cerebras" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "nvidia-nim" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "together" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "vertex-gemini" })).toBeInTheDocument();
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

  it("onboards a Vertex model manually without credentials or discovery", async () => {
    const calls: Array<{ path: string; method?: string; body?: Record<string, unknown> }> = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const path = String(input);
      const body = options?.body ? JSON.parse(String(options.body)) as Record<string, unknown> : undefined;
      calls.push({ path, method: options?.method, body });
      if (!options?.method && path === "/admin/v1/providers") return json({ data: [{ id: "vertex", type: "vertex-gemini", base_url: "https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google", auth_type: "gcp_adc", enabled: true }] });
      if (!options?.method && path === "/admin/v1/credentials") return json({ data: [{ id: "unused", provider_id: "vertex" }] });
      if (!options?.method && path === "/admin/v1/model-catalog") return json({ version: "v1", models: [] });
      if (!options?.method && path === "/admin/v1/model-groups") return json({ data: [] });
      if (!options?.method && path === "/admin/v1/provider-capabilities") return json({ data: [{ type: "vertex-gemini", operations: ["chat", "count_tokens", "stream"], capabilities: ["chat", "stream", "tools"] }] });
      if (path === "/admin/v1/model-onboarding/plan") return json({ revision: 3, catalog_version: "v1", deployments: body?.deployments, model_groups: body?.model_groups, changes: [] });
      return json({ error: { message: `Unexpected ${path}` } }, 500);
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<MemoryRouter initialEntries={["/model-onboarding?provider_id=vertex"]}><AuthProvider><ModelOnboardingPage /></AuthProvider></MemoryRouter>);

    await screen.findByRole("option", { name: "vertex — vertex-gemini" });
    expect(screen.queryByLabelText("Credential")).not.toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Upstream model"), "gemini-embedding-001");
    await userEvent.click(screen.getByRole("button", { name: "Configure model" }));
    expect(await screen.findByText("Configured model")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Review 1 model(s)" }));
    await screen.findByText("Review onboarding plan");

    expect(calls.some((call) => call.path.endsWith("/test") || call.path.endsWith("/discover-models"))).toBe(false);
    const plan = calls.find((call) => call.path === "/admin/v1/model-onboarding/plan")?.body as { deployments?: Array<Record<string, unknown>> };
    expect(plan.deployments?.[0]).toMatchObject({ provider_id: "vertex", credential_id: "", upstream_model: "gemini-embedding-001", capabilities: ["embeddings"] });
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
