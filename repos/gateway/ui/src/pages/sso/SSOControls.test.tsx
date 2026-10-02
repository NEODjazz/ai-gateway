import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider } from "../../auth/AuthContext";
import { SSOSettingsPage } from "../SSOSettingsPage";
import { SSOPreset } from "./SSOPreset";
import { SSOMappings, parseMappings } from "./SSOMappings";
import { useState } from "react";

const profile = { issuer: "https://idp.example", audience: "console", client_id: "console", authorization_url: "https://idp.example/authorize", token_url: "https://idp.example/token", jwks_url: "https://idp.example/jwks", redirect_url: "https://gateway.example/auth/sso/callback", scopes: ["openid"], roles_claim: "roles", role_mappings: { "ops": "admin" }, session_ttl_seconds: 3600 };
const settings = { revision: 1, active: null, draft: profile, can_rollback: false, test_status: "not_started", key_session: true };
const connections = [{ id: "default", name: "Default", provider: "oidc", ...settings }, { id: "tenant-a", name: "Tenant A", organization_id: "org-a", provider: "entra", ...settings }];
const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
function show() { sessionStorage.setItem("ai-gateway.admin-token", "fixture"); return render(<MemoryRouter><AuthProvider><SSOSettingsPage /></AuthProvider></MemoryRouter>); }

describe("SSO visual controls", () => {
  it("retains duplicate rows and refuses to serialize them as permissions", async () => {
    function Editor() { const [value, set] = useState('{"ops":"user"}'); return <><SSOMappings kind="Group" value={value} tenant onChange={set} /><output>{value}</output></>; }
    render(<Editor />);
    await userEvent.click(screen.getByRole("button", { name: "Add group mapping" }));
    await userEvent.type(screen.getByLabelText("IdP group 2"), "ops");
    const value = screen.getByRole("status").textContent!;
    expect(() => parseMappings(value, "Group", true)).toThrow(/unique IdP/);
    expect(screen.getByLabelText("IdP group 1")).toHaveValue("ops");
    expect(screen.getByLabelText("IdP group 2")).toHaveValue("ops");
    expect(within(screen.getByLabelText("Gateway role for group 1")).queryByRole("option", { name: "admin" })).not.toBeInTheDocument();
    expect(() => parseMappings('{"ops":"admin"}', "Role", true)).toThrow(/invalid/);
  });
  it("requires a specific Entra tenant and discovers only after explicit preset application", async () => {
    const apply = vi.fn(); render(<SSOPreset provider="entra" disabled={false} onApply={apply} />);
    await userEvent.type(screen.getByLabelText("Entra tenant ID"), "common");
    await userEvent.click(screen.getByRole("button", { name: "Apply preset" }));
    expect(apply).not.toHaveBeenCalled(); expect(screen.getByRole("alert")).toHaveTextContent(/specific Entra/);
    await userEvent.clear(screen.getByLabelText("Entra tenant ID"));
    await userEvent.type(screen.getByLabelText("Entra tenant ID"), "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee");
    await userEvent.click(screen.getByRole("button", { name: "Apply preset" }));
    expect(apply).toHaveBeenCalledWith({ issuer: "https://login.microsoftonline.com/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee/v2.0", roles_claim: "roles", groups_claim: "groups" });
  });
  it("rejects insecure Keycloak hosts and preserves the realm role path", async () => {
    const apply = vi.fn(); render(<SSOPreset provider="keycloak" disabled={false} onApply={apply} />);
    await userEvent.type(screen.getByLabelText("Keycloak base URL"), "http://idp.example");
    await userEvent.type(screen.getByLabelText("Keycloak realm"), "gateway");
    await userEvent.click(screen.getByRole("button", { name: "Apply preset" })); expect(apply).not.toHaveBeenCalled();
    await userEvent.clear(screen.getByLabelText("Keycloak base URL"));
    await userEvent.type(screen.getByLabelText("Keycloak base URL"), "https://idp.example/auth/");
    await userEvent.click(screen.getByRole("button", { name: "Apply preset" }));
    expect(apply).toHaveBeenCalledWith({ issuer: "https://idp.example/auth/realms/gateway", roles_claim: "realm_access.roles", groups_claim: "groups" });
  });
  it("clears secrets and ignores late default loads after selecting another connection", async () => {
    let resolve: ((value: Response) => void) | undefined;
    vi.spyOn(globalThis, "fetch").mockImplementation(async (url) => {
      if (String(url).endsWith("connections")) return json({ data: connections });
      if (String(url).includes("connection=tenant-a")) return json({ ...settings, draft: { ...profile, issuer: "https://tenant.example", organization_id: "org-a", role_mappings: { "ops": "org_admin" } } });
      return new Promise<Response>((done) => { resolve = done; });
    });
    show(); await screen.findByRole("button", { name: "Configure Tenant A" });
    await userEvent.click(screen.getByRole("button", { name: "Configure Tenant A" }));
    await screen.findByDisplayValue("https://tenant.example");
    await act(async () => resolve!(json(settings)));
    expect(screen.getByLabelText("Issuer URL")).toHaveValue("https://tenant.example");
    await userEvent.type(screen.getByLabelText("Client secret"), "fixture-secret");
    await userEvent.click(screen.getByRole("button", { name: "Configure Default" }));
    await act(async () => resolve!(json(settings)));
    await screen.findByDisplayValue(profile.issuer);
    expect(screen.getByLabelText("Client secret")).toHaveValue("");
  });
  it("saves only the selected organization and displays unapproved identity without enabling activation", async () => {
    const bodies: Record<string, unknown>[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (url, options) => {
      if (String(url).endsWith("connections")) return json({ data: connections });
      const tenant = String(url).includes("connection=tenant-a");
      if (options?.method === "PUT") bodies.push(JSON.parse(String(options.body)));
      return json({ ...settings, draft: { ...profile, organization_id: tenant ? "org-a" : undefined, role_mappings: { ops: tenant ? "org_admin" : "admin" } }, verified_identity: tenant ? { issuer: profile.issuer, audience: profile.client_id, subject: "verified-subject", user_id: "", roles: [], approved: false, verified_at: 1 } : undefined });
    });
    show(); await screen.findByRole("button", { name: "Configure Tenant A" });
    await userEvent.click(screen.getByRole("button", { name: "Configure Tenant A" }));
    await screen.findByRole("region", { name: "Verified identity" });
    expect(screen.getByRole("button", { name: "Activate SSO" })).toBeDisabled();
    expect(screen.getByText(/does not permit activation/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(bodies[0]?.organization_id).toBe("org-a"));
    const form = screen.getByRole("form", { name: "SSO principal binding" });
    await userEvent.click(within(form).getByRole("button", { name: "Use verified subject" }));
    expect(within(form).getByLabelText("IdP subject (sub)")).toHaveValue("verified-subject");
  });
});
