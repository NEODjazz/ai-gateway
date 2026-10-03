import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { SSOSettingsPage } from "./SSOSettingsPage";

const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const profile = { issuer: "https://idp.example", audience: "console", client_id: "console", authorization_url: "https://idp.example/authorize", token_url: "https://idp.example/token", jwks_url: "https://idp.example/jwks", redirect_url: "https://gateway.example/auth/sso/callback", scopes: ["openid", "profile"], roles_claim: "roles", role_mappings: { "gateway-admin": "admin" }, session_ttl_seconds: 3600, client_secret_configured: true };
const settings = { revision: 1, active: null, draft: profile, can_rollback: false, test_status: "not_started", key_session: true };
function show() {
  const impl = vi.mocked(globalThis.fetch).getMockImplementation()!;
  vi.mocked(globalThis.fetch).mockImplementation((url, options) => String(url).endsWith("/sso/connections") ? Promise.resolve(json({ data: [{ id: "default", name: "Default", provider: "oidc", ...settings }] })) : impl(url, options));
  sessionStorage.setItem("ai-gateway.admin-token", "fixture"); return render(<MemoryRouter><AuthProvider><SSOSettingsPage /></AuthProvider></MemoryRouter>); }

async function edit() {
  await userEvent.click(await screen.findByRole("button", { name: "Edit SSO settings" }));
  const dialog = await screen.findByRole("dialog", { name: "Edit SSO settings" });
  await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
  await userEvent.click(screen.getByText("Advanced OIDC endpoints"));
}

describe("SSOSettingsPage", () => {
  it("limits mappings to tenant roles for an organization-bound default profile", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json({ ...settings, draft: { ...profile, organization_id: "org-a", role_mappings: { owners: "org_admin" }, groups_claim: "groups", group_mappings: { operators: "org_admin" } } }));
    show(); await edit(); await screen.findByDisplayValue(profile.issuer);
    for (const kind of ["role", "group"]) {
      const choices = within(screen.getByLabelText(`Gateway role for ${kind} 1`));
      expect(choices.getByRole("option", { name: "org_admin" })).toBeInTheDocument();
      expect(choices.queryByRole("option", { name: "admin" })).not.toBeInTheDocument();
      expect(choices.queryByRole("option", { name: "team_admin" })).not.toBeInTheDocument();
    }
  });
	it("saves the browser client audience independently of legacy API audience and explains independent trust", async () => {
		let body: Record<string, unknown> | undefined;
		vi.spyOn(globalThis, "fetch").mockImplementation(async (_url, options) => {
			if (options?.method === "PUT") { body = JSON.parse(String(options.body)); return json(settings); }
			return json({ ...settings, draft: { ...profile, audience: "legacy-api-resource" } });
		});
		show(); await edit(); await screen.findByDisplayValue(profile.issuer);
		expect(screen.getByText(/Browser SSO settings are independent of API JWT trust/, { selector: "p" })).toBeInTheDocument();
		await userEvent.type(screen.getByLabelText("Trusted additional endpoint origins"), "https://tokens.example, https://keys.example");
		await userEvent.click(screen.getByRole("button", { name: "Save draft" }));
		await waitFor(() => expect(body?.audience).toBe(profile.client_id));
		expect(body?.endpoint_origins).toEqual(["https://tokens.example", "https://keys.example"]);
	});
  it("disables activation as soon as the verification proof expires", async () => {
    vi.useFakeTimers();
    let view: ReturnType<typeof show> | undefined;
    try {
      vi.spyOn(globalThis, "fetch").mockResolvedValue(json({ ...settings, test_status: "passed", test_expires_at: Math.floor(Date.now() / 1000) + 1 }));
      await act(async () => { view = show(); });
      expect(screen.getByRole("button", { name: "Activate SSO", hidden: true })).toBeEnabled();
      act(() => vi.advanceTimersByTime(1001));
      expect(screen.getByRole("button", { name: "Activate SSO", hidden: true })).toBeDisabled();
      expect(screen.getByText("expired", { exact: true })).toBeInTheDocument();
    } finally { view?.unmount(); vi.useRealTimers(); }
  });
  it("preserves a configured secret when omitted, clears it explicitly and invalidates test controls while editing", async () => {
    const bodies: Record<string, unknown>[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (_url, options) => {
      if (options?.method === "PUT") { const body = JSON.parse(String(options.body)); bodies.push(body); return json({ ...settings, revision: bodies.length + 1 }); }
      return json(settings);
    });
    show(); await edit(); await screen.findByDisplayValue(profile.issuer);
    expect(screen.getByRole("button", { name: "Activate SSO", hidden: true })).toBeDisabled();
    await userEvent.clear(screen.getByLabelText("Scopes"));
    await userEvent.type(screen.getByLabelText("Scopes"), "openid custom.scope");
    await userEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).not.toHaveProperty("client_secret"); expect(bodies[0]).not.toHaveProperty("client_secret_configured");
    expect(bodies[0].scopes).toEqual(["openid", "custom.scope"]);
    await edit();
    await userEvent.click(screen.getByLabelText("Clear saved client secret"));
    expect(screen.getByRole("button", { name: "Test sign-in", hidden: true })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(bodies).toHaveLength(2));
    expect(bodies[1].client_secret).toBe("");
    await edit();
    expect(screen.getByLabelText("Client secret")).toHaveValue("");
  });
  it("allows activation only after a fresh proof and prevents JWT sessions from changing trust", async () => {
    let current = { ...settings, key_session: false, test_status: "passed", test_expires_at: Math.floor(Date.now() / 1000) + 300 };
    const calls: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (url) => { calls.push(String(url)); return json(current); });
    show(); await screen.findByRole("button", { name: "Edit SSO settings" });
    expect(screen.getByRole("button", { name: "Activate SSO", hidden: true })).toBeDisabled();
    expect(screen.getByText(/Sign in with an administrator virtual key/)).toBeInTheDocument();
    current = { ...current, key_session: true };
    await userEvent.click(screen.getByRole("button", { name: "Refresh test status", hidden: true }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Activate SSO", hidden: true })).toBeEnabled());
    await edit();
    await userEvent.type(screen.getByLabelText("Client ID"), "-edited");
    expect(screen.getByLabelText("Client ID")).toHaveValue("console-edited");
    expect(screen.getByRole("button", { name: "Activate SSO", hidden: true })).toBeDisabled();
    expect(calls).not.toContain("/admin/v1/sso/action");
  });
  it("loads discovery into the draft and validates role mappings before sending secrets", async () => {
    const methods: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (url, options) => {
      methods.push(options?.method || "GET");
      return json(String(url).endsWith("discover") ? { issuer: profile.issuer, authorization_url: profile.issuer + "/new-auth", token_url: profile.issuer + "/new-token", jwks_url: profile.issuer + "/new-jwks" } : settings);
    });
    show(); await edit(); await screen.findByDisplayValue(profile.issuer);
    await userEvent.click(screen.getByRole("button", { name: "Discover endpoints" }));
    await screen.findByDisplayValue(profile.issuer + "/new-token");
    await userEvent.click(screen.getByText("Advanced role mappings"));
    await userEvent.clear(screen.getByLabelText("Role mappings (JSON)"));
    await userEvent.type(screen.getByLabelText("Role mappings (JSON)"), "invalid-json");
    await userEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("Role mappings must be a JSON object.")).toBeInTheDocument();
    expect(methods).not.toContain("PUT");
  });
  it("starts a test without activating trust and refreshes status without overwriting edits", async () => {
    const replace = vi.fn(); const close = vi.fn();
    vi.spyOn(window, "open").mockReturnValue({ opener: {}, location: { replace }, close } as unknown as Window);
    let reads = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation(async (url) => {
      if (String(url).endsWith("/test")) return json({ start_url: "/auth/sso/test/start?ticket=fixture" });
      return json({ ...settings, test_status: reads++ ? "running" : "not_started" });
    });
    show(); await screen.findByRole("button", { name: "Edit SSO settings" });
    await userEvent.click(screen.getByRole("button", { name: "Test sign-in" }));
    await waitFor(() => expect(replace).toHaveBeenCalledWith("/auth/sso/test/start?ticket=fixture"));
    await screen.findByText("running");
    await edit();
    await userEvent.type(screen.getByLabelText("Client ID"), "-edited");
    await userEvent.click(screen.getByRole("button", { name: "Refresh test status", hidden: true }));
    expect(screen.getByLabelText("Client ID")).toHaveValue("console-edited");
  });
  it("configures an explicit principal binding with no implicit inference grants", async () => {
    let binding: Record<string, unknown> | undefined;
    vi.spyOn(globalThis, "fetch").mockImplementation(async (url, options) => {
      if (String(url).endsWith("jwt-principals")) { binding = JSON.parse(String(options?.body)); return json(binding); }
      return json(settings);
    });
    show(); await userEvent.click(await screen.findByRole("button", { name: "Bind identity to a Gateway user" }));
    const form = screen.getByRole("form", { name: "SSO principal binding" });
    await userEvent.type(within(form).getByLabelText("Internal user ID"), "admin-user");
    await userEvent.type(within(form).getByLabelText("IdP subject (sub)"), "immutable-subject");
    await userEvent.click(screen.getByRole("button", { name: "Save principal binding" }));
    await waitFor(() => expect(binding).toEqual({ issuer: profile.issuer, audience: "console", subject: "immutable-subject", user_id: "admin-user", enabled: true, allowed_models: [], allowed_tools: [] }));
  });
  it("shows backend failures without a false configured state", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json({ error: { message: "Auth unavailable" } }, 503));
    show(); expect(await screen.findByText("Auth unavailable")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Activate SSO", hidden: true })).not.toBeInTheDocument();
  });
});
