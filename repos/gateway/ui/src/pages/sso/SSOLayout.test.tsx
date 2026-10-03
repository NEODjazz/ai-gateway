import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider } from "../../auth/AuthContext";
import { SSOSettingsPage } from "../SSOSettingsPage";
import { CopyValue } from "./SSODetails";

const json = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status });
const profile = { issuer: "https://idp.example", client_id: "console", audience: "console", authorization_url: "https://idp.example/authorize", token_url: "https://idp.example/token", jwks_url: "https://idp.example/jwks", redirect_url: "https://gateway.example/auth/sso/callback", scopes: ["openid"], roles_claim: "roles", role_mappings: { operators: "org_admin" }, groups_claim: "groups", group_mappings: { engineers: "developer" }, organization_id: "org-a", session_ttl_seconds: 3600, client_secret_configured: true };
const settings = { revision: 1, active: null, draft: profile, key_session: true, can_rollback: false, test_status: "not_started" };
function show(view: unknown = settings, mutate?: (path: string, options: RequestInit) => Promise<Response>) {
  const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(async (path, options) => {
    if (options?.method && mutate) return mutate(String(path), options);
    if (String(path).endsWith("/sso/connections")) return json({ data: [{ id: "default", name: "Default", provider: "oidc", ...settings }] });
    if (String(path).endsWith("/api-issuers")) return json({ data: [], key_session: true });
    return json(view);
  });
  sessionStorage.setItem("ai-gateway.admin-token", "fixture");
  render(<MemoryRouter><AuthProvider><SSOSettingsPage /></AuthProvider></MemoryRouter>);
  return fetch;
}

describe("SSO settings organization", () => {
  it("shows saved configuration and mappings without exposing edit or secret fields", async () => {
    show(); await screen.findByRole("button", { name: "Edit SSO settings" });
    expect(screen.queryByRole("form")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Client secret")).not.toBeInTheDocument();
    expect(screen.getByText("Configured", { exact: true })).toBeInTheDocument();
    expect(screen.getByText(/Saved draft · not yet active/)).toBeInTheDocument();
    const mappings = screen.getByRole("region", { name: "Saved role mappings" });
    expect(within(mappings).getByRole("cell", { name: "operators" })).toBeInTheDocument();
    expect(within(mappings).getByRole("cell", { name: "engineers" })).toBeInTheDocument();
  });
  it("distinguishes an inactive draft from the actual active issuer", async () => {
    show({ ...settings, active: { ...profile, enabled: true, issuer: "https://active.example" } });
    expect(await screen.findByText(/Browser SSO enabled: https:\/\/active.example/)).toBeInTheDocument();
    expect(screen.getByText(/The draft below has not changed the active profile/)).toBeInTheDocument();
  });
  it("opens setup from an empty state without inventing a configured connection", async () => {
    show({ ...settings, draft: null });
    await userEvent.click(await screen.findByRole("button", { name: "Set up SSO" }));
    expect(screen.getByRole("dialog", { name: "Set up SSO" })).toBeInTheDocument();
    expect(screen.getByLabelText("Issuer URL")).toHaveValue("");
    expect(screen.getByLabelText("Client secret")).toHaveValue("");
  });
  it("contains keyboard focus and restores it after cancelling a clean draft", async () => {
    show(); const opener = await screen.findByRole("button", { name: "Edit SSO settings" });
    await userEvent.click(opener);
    const dialog = screen.getByRole("dialog", { name: "Edit SSO settings" });
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    for (let n = 0; n < 5; n++) { await userEvent.tab(); expect(dialog.contains(document.activeElement)).toBe(true); }
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(opener).toHaveFocus();
  });
  it("confirms discarding edits and clears write-only secrets before reopening", async () => {
    const fetch = show(); await userEvent.click(await screen.findByRole("button", { name: "Edit SSO settings" }));
    await userEvent.type(screen.getByLabelText("Client secret"), "synthetic-secret");
    await userEvent.type(screen.getByLabelText("Client ID"), "-changed");
    await userEvent.keyboard("{Escape}");
    expect(screen.getByText("Discard unsaved changes?")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Continue editing" }));
    expect(screen.getByLabelText("Client ID")).toHaveValue("console-changed");
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await userEvent.click(screen.getByRole("button", { name: "Discard changes" }));
    await userEvent.click(await screen.findByRole("button", { name: "Edit SSO settings" }));
    expect(screen.getByLabelText("Client ID")).toHaveValue("console");
    expect(screen.getByLabelText("Client secret")).toHaveValue("");
    expect(fetch.mock.calls.every(([, options]) => !options?.method)).toBe(true);
  });
  it("retains edits on save failure, blocks closing during a save and preserves organization", async () => {
    let resolve!: (response: Response) => void;
    let body: Record<string, unknown> | undefined;
    show(settings, async (_path, options) => { body = JSON.parse(String(options.body)); return new Promise((done) => { resolve = done; }); });
    await userEvent.click(await screen.findByRole("button", { name: "Edit SSO settings" }));
    await userEvent.type(screen.getByLabelText("Client ID"), "-edited");
    await userEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(screen.getByRole("button", { name: "Close dialog" })).toBeDisabled();
    await userEvent.keyboard("{Escape}");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    await act(async () => resolve(json({ error: { message: "Revision changed" } }, 409)));
    expect(await screen.findByText("Revision changed")).toBeInTheDocument();
    expect(screen.getByLabelText("Client ID")).toHaveValue("console-edited");
    expect(body?.organization_id).toBe("org-a");
  });
  it("creates connections in a separate dialog and retains backend validation failures", async () => {
    show(settings, async () => json({ error: { message: "Organization is inactive" } }, 400));
    await userEvent.click(await screen.findByRole("button", { name: "Add connection" }));
    expect(screen.getByRole("dialog", { name: "Add SSO connection" })).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Connection ID"), "org-a");
    await userEvent.type(screen.getByLabelText("Connection name"), "Operations");
    await userEvent.type(screen.getByLabelText("Organization ID"), "org-a");
    await userEvent.click(screen.getByRole("button", { name: "Create connection" }));
    expect(await screen.findByText("Organization is inactive")).toBeInTheDocument();
    expect(screen.getByLabelText("Connection name")).toHaveValue("Operations");
  });
  it("navigates to API JWT independently and does not load its inventory before selection", async () => {
    const fetch = show(); await screen.findByRole("button", { name: "Edit SSO settings" });
    expect(fetch.mock.calls.some(([path]) => String(path).includes("api-issuers"))).toBe(false);
    await userEvent.click(screen.getByRole("tab", { name: "API JWT" }));
    await screen.findByRole("heading", { name: "API JWT issuers" });
    expect(screen.queryByRole("region", { name: "SSO connections" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Browser SSO" }));
    expect(await screen.findByRole("region", { name: "SSO connections" })).toBeInTheDocument();
  });
});

it("copies the exact callback and reports clipboard failures truthfully", async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
  render(<CopyValue label="login callback" value={profile.redirect_url} />);
  await userEvent.click(screen.getByRole("button", { name: "Copy login callback" }));
  expect(writeText).toHaveBeenCalledWith(profile.redirect_url);
  expect(await screen.findByText("Copied")).toBeInTheDocument();
  writeText.mockRejectedValueOnce(new Error("Unavailable"));
  await userEvent.click(screen.getByRole("button", { name: "Copy login callback" }));
  expect(await screen.findByText(/Could not copy/)).toBeInTheDocument();
  expect(screen.queryByText("Copied")).not.toBeInTheDocument();
});
