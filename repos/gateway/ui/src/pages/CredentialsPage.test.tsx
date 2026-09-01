import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { CredentialsPage } from "./CredentialsPage";

const json = (payload: unknown, status = 200) => new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } });

describe("CredentialsPage", () => {
  it("updates metadata without a secret and rotates through the dedicated operation", async () => {
    const calls: Array<{ path: string; method?: string; body?: string }> = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const path = String(input); calls.push({ path, method: options?.method, body: String(options?.body || "") });
      if (path === "/admin/v1/credentials" && !options?.method) return json({ data: [{ id: "azure-key", provider_id: "azure", description: "Primary", created_at: "2026-09-01T10:00:00Z", updated_at: "2026-09-01T11:00:00Z" }] });
      if (path === "/admin/v1/providers") return json({ data: [{ id: "azure", type: "openai-compatible", enabled: true }, { id: "ollama", type: "ollama", enabled: true }] });
      if (path === "/admin/v1/model-deployments") return json({ data: [{ id: "azure-gpt", provider_id: "azure", credential_id: "azure-key" }] });
      if (path === "/admin/v1/credentials/azure-key" && options?.method === "PUT") return json({ id: "azure-key", provider_id: "azure", description: "Updated" });
      if (path === "/admin/v1/credentials/azure-key/rotate" && options?.method === "POST") return json({ id: "azure-key", provider_id: "azure", description: "Updated" });
      return json({ data: [] });
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<AuthProvider><CredentialsPage /></AuthProvider>);

    expect(await screen.findByText("azure-key")).toBeInTheDocument();
    expect(screen.getByText("Bound")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Actions for azure-key" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Edit metadata" }));
    const edit = screen.getByRole("dialog", { name: "Edit credential metadata" });
    const description = within(edit).getByLabelText("Description");
    await userEvent.clear(description); await userEvent.type(description, "Updated");
    await userEvent.click(within(edit).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls.some((call) => call.path === "/admin/v1/credentials/azure-key" && call.method === "PUT")).toBe(true));
    const metadata = JSON.parse(calls.find((call) => call.path === "/admin/v1/credentials/azure-key" && call.method === "PUT")!.body!);
    expect(metadata).toEqual({ provider_id: "azure", description: "Updated" });
    expect(metadata).not.toHaveProperty("secret");

    await userEvent.click(screen.getByRole("button", { name: "Actions for azure-key" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Rotate secret" }));
    const rotate = screen.getByRole("dialog", { name: "Rotate credential secret" });
    await userEvent.type(within(rotate).getByLabelText("New secret"), "replacement-secret");
    await userEvent.type(within(rotate).getByLabelText("Confirm secret"), "replacement-secret");
    await userEvent.click(within(rotate).getByRole("button", { name: "Rotate secret" }));
    await waitFor(() => expect(calls.some((call) => call.path.endsWith("/rotate") && call.method === "POST")).toBe(true));
    expect(JSON.parse(calls.find((call) => call.path.endsWith("/rotate"))!.body!)).toEqual({ secret: "replacement-secret" });
    expect(await screen.findByText(/secret rotated/)).toBeInTheDocument();
    expect(screen.queryByDisplayValue("replacement-secret")).not.toBeInTheDocument();
  });

  it("creates a provider-bound write-only credential", async () => {
    const calls: Array<{ path: string; method?: string; body?: string }> = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const path = String(input); calls.push({ path, method: options?.method, body: String(options?.body || "") });
      if (path === "/admin/v1/providers") return json({ data: [{ id: "azure", type: "openai-compatible", enabled: true }] });
      if (path === "/admin/v1/credentials" && options?.method === "POST") return json({ id: "new-key", provider_id: "azure", description: "Secondary", created_at: "2026-09-01T10:00:00Z", updated_at: "2026-09-01T10:00:00Z" }, 201);
      return json({ data: [] });
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<AuthProvider><CredentialsPage /></AuthProvider>);
    await userEvent.click(await screen.findByRole("button", { name: "Create Credential" }));
    const form = screen.getByRole("dialog", { name: "Create Credential" });
    await userEvent.type(within(form).getByLabelText("Credential ID"), "new-key");
    await userEvent.selectOptions(within(form).getByLabelText("Credential provider"), "azure");
    await userEvent.type(within(form).getByLabelText("Description"), "Secondary");
    await userEvent.type(within(form).getByLabelText("New secret"), "secret-value");
    await userEvent.type(within(form).getByLabelText("Confirm secret"), "secret-value");
    await userEvent.click(within(form).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls.some((call) => call.path === "/admin/v1/credentials" && call.method === "POST")).toBe(true));
    expect(JSON.parse(calls.find((call) => call.path === "/admin/v1/credentials" && call.method === "POST")!.body!)).toEqual({ id: "new-key", provider_id: "azure", description: "Secondary", secret: "secret-value" });
    expect(await screen.findByText(/remains write-only/)).toBeInTheDocument();
    expect(screen.queryByDisplayValue("secret-value")).not.toBeInTheDocument();
  });
});
