import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { ProvidersPage } from "./ProvidersPage";

const json = (payload: unknown, status = 200) => new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } });

function LocationProbe() {
  const location = useLocation();
  return <output>{`${location.pathname}${location.search}`}</output>;
}

describe("ProvidersPage", () => {
  it("selects a matching credential for test and discovery, then links to onboarding", async () => {
    const calls: Array<{ path: string; method?: string; body?: string }> = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const path = String(input); calls.push({ path, method: options?.method, body: String(options?.body || "") });
      if (path === "/admin/v1/providers") return json({ data: [{ id: "azure", type: "openai-compatible", base_url: "https://example.test", enabled: true }] });
      if (path === "/admin/v1/credentials") return json({ data: [{ id: "azure-key", provider_id: "azure", description: "Primary" }, { id: "other-key", provider_id: "other" }] });
      if (path === "/admin/v1/model-deployments") return json({ data: [{ id: "azure-gpt", provider_id: "azure", enabled: true }] });
      if (path.endsWith("/test")) return json({ provider_id: "azure", status: "available", latency_ms: 42, model_count: 2 });
      if (path.endsWith("/discover-models")) return json({ data: [{ id: "gpt-a" }, { id: "gpt-b" }] });
      return json({ data: [] });
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<MemoryRouter initialEntries={["/providers"]}><AuthProvider><Routes><Route path="/providers" element={<ProvidersPage />} /><Route path="/model-onboarding" element={<LocationProbe />} /></Routes></AuthProvider></MemoryRouter>);

    expect(await screen.findByText("azure")).toBeInTheDocument();
    expect(screen.getByText("https://example.test")).toBeInTheDocument();
    expect(screen.getAllByText("1").length).toBeGreaterThanOrEqual(2);
    await userEvent.click(screen.getByRole("button", { name: "Actions for azure" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Test connection" }));
    const testDialog = screen.getByRole("dialog", { name: "Test provider connection" });
    expect(within(testDialog).getByLabelText("Provider credential")).toHaveValue("azure-key");
    expect(within(testDialog).queryByRole("option", { name: /other-key/ })).not.toBeInTheDocument();
    await userEvent.click(within(testDialog).getByRole("button", { name: "Test connection" }));
    expect(await within(testDialog).findByText("Available")).toBeInTheDocument();
    expect(within(testDialog).getByText("42 ms")).toBeInTheDocument();
    await userEvent.click(within(testDialog).getByRole("button", { name: "Close" }));

    await userEvent.click(screen.getByRole("button", { name: "Actions for azure" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Discover models" }));
    const discoveryDialog = screen.getByRole("dialog", { name: "Discover provider models" });
    await userEvent.click(within(discoveryDialog).getByRole("button", { name: "Discover models" }));
    expect(await within(discoveryDialog).findByText("gpt-a")).toBeInTheDocument();
    expect(within(discoveryDialog).getByText("2 models discovered")).toBeInTheDocument();
    await userEvent.click(within(discoveryDialog).getByRole("button", { name: "Continue to onboarding" }));
    expect(await screen.findByText("/model-onboarding?provider_id=azure&credential_id=azure-key")).toBeInTheDocument();
    await waitFor(() => expect(calls.filter((call) => call.path.endsWith("/test"))).toHaveLength(1));
    expect(JSON.parse(calls.find((call) => call.path.endsWith("/test"))!.body!)).toEqual({ credential_id: "azure-key" });
    expect(JSON.parse(calls.find((call) => call.path.endsWith("/discover-models"))!.body!)).toEqual({ credential_id: "azure-key" });
  });

  it("creates a provider with the managed form", async () => {
    const calls: Array<{ path: string; method?: string; body?: string }> = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const path = String(input); calls.push({ path, method: options?.method, body: String(options?.body || "") });
      if (path === "/admin/v1/providers" && options?.method === "POST") return json({ id: "managed-router", type: "openrouter", base_url: "https://api.example.test", enabled: true }, 201);
      return json({ data: [] });
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<MemoryRouter><AuthProvider><ProvidersPage /></AuthProvider></MemoryRouter>);
    await userEvent.click(await screen.findByRole("button", { name: "Create Provider" }));
    const form = screen.getByRole("dialog", { name: "Create Provider" });
    expect(within(form).getByRole("option", { name: "voyage" })).toBeInTheDocument();
    expect(within(form).getByRole("option", { name: "openrouter" })).toBeInTheDocument();
    await userEvent.type(within(form).getByLabelText("ID"), "managed-router");
    await userEvent.selectOptions(within(form).getByLabelText("Type"), "openrouter");
    await userEvent.type(within(form).getByLabelText("Base URL"), "https://api.example.test");
    await userEvent.type(within(form).getByLabelText("Shared requests per minute"), "120");
    await userEvent.type(within(form).getByLabelText("Shared tokens per minute"), "64000");
    await userEvent.click(within(form).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls.some((call) => call.path === "/admin/v1/providers" && call.method === "POST")).toBe(true));
    const created = calls.find((call) => call.path === "/admin/v1/providers" && call.method === "POST")!;
    expect(JSON.parse(created.body!)).toEqual({ id: "managed-router", type: "openrouter", base_url: "https://api.example.test", rate_limit_rpm: 120, rate_limit_tpm: 64000, enabled: true });
  });

  it("configures native Azure endpoint version and authentication", async () => {
	const calls: Array<{ path: string; method?: string; body?: string }> = [];
	vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
	  const path = String(input); calls.push({ path, method: options?.method, body: String(options?.body || "") });
	  if (path === "/admin/v1/providers" && options?.method === "POST") return json({ id: "azure-native", type: "azure-openai", base_url: "https://resource.openai.azure.com", api_version: "2025-04-01-preview", auth_type: "entra", enabled: true }, 201);
	  return json({ data: [] });
	});
	sessionStorage.setItem("ai-gateway.admin-token", "token");
	render(<MemoryRouter><AuthProvider><ProvidersPage /></AuthProvider></MemoryRouter>);
	await userEvent.click(await screen.findByRole("button", { name: "Create Provider" }));
	const form = screen.getByRole("dialog", { name: "Create Provider" });
	await userEvent.type(within(form).getByLabelText("ID"), "azure-native");
	await userEvent.selectOptions(within(form).getByLabelText("Type"), "azure-openai");
	await userEvent.type(within(form).getByLabelText("Base URL"), "https://resource.openai.azure.com");
	await userEvent.type(within(form).getByLabelText("Azure API version"), "2025-04-01-preview");
	await userEvent.selectOptions(within(form).getByLabelText("Authentication"), "entra");
	await userEvent.click(within(form).getByRole("button", { name: "Save" }));
	await waitFor(() => expect(calls.some((call) => call.path === "/admin/v1/providers" && call.method === "POST")).toBe(true));
	const created = calls.find((call) => call.path === "/admin/v1/providers" && call.method === "POST")!;
	expect(JSON.parse(created.body!)).toEqual({ id: "azure-native", type: "azure-openai", base_url: "https://resource.openai.azure.com", api_version: "2025-04-01-preview", auth_type: "entra", rate_limit_rpm: 0, rate_limit_tpm: 0, enabled: true });
  });

  it("configures Bedrock SigV4 authentication and region", async () => {
	const calls: Array<{ path: string; method?: string; body?: string }> = [];
	vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
	  const path = String(input); calls.push({ path, method: options?.method, body: String(options?.body || "") });
	  if (path === "/admin/v1/providers" && options?.method === "POST") return json({ id: "aws-bedrock", type: "bedrock", base_url: "https://bedrock-runtime.us-east-1.amazonaws.com", auth_type: "aws_sigv4", region: "us-east-1", enabled: true }, 201);
	  return json({ data: [] });
	});
	sessionStorage.setItem("ai-gateway.admin-token", "token");
	render(<MemoryRouter><AuthProvider><ProvidersPage /></AuthProvider></MemoryRouter>);
	await userEvent.click(await screen.findByRole("button", { name: "Create Provider" }));
	const form = screen.getByRole("dialog", { name: "Create Provider" });
	await userEvent.type(within(form).getByLabelText("ID"), "aws-bedrock");
	await userEvent.selectOptions(within(form).getByLabelText("Type"), "bedrock");
	await userEvent.type(within(form).getByLabelText("Base URL"), "https://bedrock-runtime.us-east-1.amazonaws.com");
	await userEvent.selectOptions(within(form).getByLabelText("Authentication"), "aws_sigv4");
	await userEvent.type(within(form).getByLabelText("AWS region"), "us-east-1");
	await userEvent.click(within(form).getByRole("button", { name: "Save" }));
	await waitFor(() => expect(calls.some((call) => call.path === "/admin/v1/providers" && call.method === "POST")).toBe(true));
	const created = calls.find((call) => call.path === "/admin/v1/providers" && call.method === "POST")!;
	expect(JSON.parse(created.body!)).toEqual({ id: "aws-bedrock", type: "bedrock", base_url: "https://bedrock-runtime.us-east-1.amazonaws.com", auth_type: "aws_sigv4", region: "us-east-1", rate_limit_rpm: 0, rate_limit_tpm: 0, enabled: true });
  });

  it("configures Gemini workload authentication", async () => {
    const calls: Array<{ path: string; method?: string; body?: string }> = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const path = String(input); calls.push({ path, method: options?.method, body: String(options?.body || "") });
      if (path === "/admin/v1/providers" && options?.method === "POST") return json({ id: "google-gemini", type: "gemini", base_url: "https://generativelanguage.googleapis.com", auth_type: "gcp_adc", enabled: true }, 201);
      return json({ data: [] });
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<MemoryRouter><AuthProvider><ProvidersPage /></AuthProvider></MemoryRouter>);
    await userEvent.click(await screen.findByRole("button", { name: "Create Provider" }));
    const form = screen.getByRole("dialog", { name: "Create Provider" });
    await userEvent.type(within(form).getByLabelText("ID"), "google-gemini");
    await userEvent.selectOptions(within(form).getByLabelText("Type"), "gemini");
    await userEvent.type(within(form).getByLabelText("Base URL"), "https://generativelanguage.googleapis.com");
    await userEvent.selectOptions(within(form).getByLabelText("Authentication"), "gcp_adc");
    await userEvent.click(within(form).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls.some((call) => call.path === "/admin/v1/providers" && call.method === "POST")).toBe(true));
    const created = calls.find((call) => call.path === "/admin/v1/providers" && call.method === "POST")!;
    expect(JSON.parse(created.body!)).toEqual({ id: "google-gemini", type: "gemini", base_url: "https://generativelanguage.googleapis.com", auth_type: "gcp_adc", rate_limit_rpm: 0, rate_limit_tpm: 0, enabled: true });
  });
});
