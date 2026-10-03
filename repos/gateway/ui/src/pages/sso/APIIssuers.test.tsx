import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../../auth/AuthContext";
import { APIIssuers } from "./APIIssuers";

const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
const config = { jwks_url: "https://api-idp.example/jwks", roles_claim: "roles", role_mappings: { owners: "org_admin", users: "user" } };
const row = { id: "tenant-a", name: "Company A API", organization_id: "org-a", issuer: "https://api-idp.example", audience: "gateway-resource", revision: 1, active: null, draft: { ...config, id: "profile", enabled: true }, can_rollback: false };
function show() { sessionStorage.setItem("ai-gateway.admin-token", "fixture-key"); return render(<AuthProvider><APIIssuers /></AuthProvider>); }

describe("API issuers", () => {
  it("keeps immutable API namespace separate and sends a bounded write-only test token", async () => {
    const calls: { path: string; body: Record<string, unknown> }[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (path, options) => {
      if (!options?.method) return json({ data: [row], key_session: true });
      const body = JSON.parse(String(options.body)); calls.push({ path: String(path), body });
      return json({ ...row, revision: 2, last_test_status: "passed", test_expires_at: Math.floor(Date.now() / 1000) + 60 });
    });
    show(); await userEvent.click(await screen.findByRole("button", { name: row.name }));
    await userEvent.click(screen.getByRole("button", { name: "Edit API issuer settings" }));
    expect(screen.getByLabelText("API issuer URL")).toHaveAttribute("readonly");
    expect(screen.getByLabelText("API resource audience")).toHaveValue("gateway-resource");
    await userEvent.type(screen.getByLabelText("Resource access token for test"), "synthetic-access-token");
    await userEvent.click(screen.getByRole("button", { name: "Test API issuer" }));
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]).toEqual({ path: "/admin/v1/api-issuers/tenant-a/test", body: { expected_revision: 1, token: "synthetic-access-token" } });
    expect(screen.getByLabelText("Resource access token for test")).toHaveValue("");
    expect(JSON.stringify(sessionStorage)).not.toContain("synthetic-access-token");
    expect(await screen.findByRole("button", { name: "Activate API issuer" })).toBeEnabled();
  });
  it("retains a failed test error and clears the token without claiming verification", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => options?.method ? json({ error: { message: "Principal approval required" } }, 403) : json({ data: [row], key_session: true }));
    show(); await userEvent.click(await screen.findByRole("button", { name: row.name }));
    await userEvent.click(screen.getByRole("button", { name: "Edit API issuer settings" }));
    await userEvent.type(screen.getByLabelText("Resource access token for test"), "synthetic-access-token");
    await userEvent.click(screen.getByRole("button", { name: "Test API issuer" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Principal approval required");
    expect(screen.getByLabelText("Resource access token for test")).toHaveValue("");
    expect(screen.getByRole("button", { name: "Activate API issuer" })).toBeDisabled();
  });
  it("requires a recovery key and does not enable mutation after a failed inventory request", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(json({ data: [row], key_session: false })).mockResolvedValue(json({ error: { message: "Auth unavailable" } }, 503));
    show(); await userEvent.click(await screen.findByRole("button", { name: row.name }));
    await userEvent.click(screen.getByRole("button", { name: "Edit API issuer settings" }));
    expect(screen.getByRole("button", { name: "Save API issuer draft" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Reload API issuers", hidden: true }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Auth unavailable");
    expect(screen.queryByRole("button", { name: row.name })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New API issuer" })).toBeDisabled();
  });
  it("does not select an old issuer after a delayed mutation resolves", async () => {
    let finish!: (response: Response) => void;
    const delayed = new Promise<Response>((resolve) => { finish = resolve; });
    vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => options?.method ? delayed : json({ data: [row, { ...row, id: "tenant-b", name: "Company B API", organization_id: "org-b" }], key_session: true }));
    show(); await userEvent.click(await screen.findByRole("button", { name: row.name }));
    await userEvent.click(screen.getByRole("button", { name: "Edit API issuer settings" }));
    await userEvent.type(screen.getByLabelText("Resource access token for test"), "synthetic-token");
    await userEvent.click(screen.getByRole("button", { name: "Test API issuer" }));
    await userEvent.click(screen.getByRole("button", { name: "Company B API", hidden: true }));
    await act(async () => { finish(json({ ...row, revision: 2 })); });
    expect(screen.getByLabelText("API issuer ID")).toHaveValue("tenant-b");
  });
  it("expires test proof visibly without another user action", async () => {
    vi.useFakeTimers(); let view: ReturnType<typeof show> | undefined;
    try {
      vi.spyOn(globalThis, "fetch").mockResolvedValue(json({ data: [{ ...row, last_test_status: "passed", test_expires_at: Math.floor(Date.now() / 1000) + 1 }], key_session: true }));
      await act(async () => { view = show(); });
      // Fire a direct click under fake timers; user-event waits on timer ticks.
      await act(async () => { screen.getByRole("button", { name: row.name }).click(); screen.getByRole("button", { name: "Edit API issuer settings" }).click(); });
      expect(screen.getByRole("button", { name: "Activate API issuer" })).toBeEnabled();
      act(() => vi.advanceTimersByTime(1001));
      expect(screen.getByRole("button", { name: "Activate API issuer" })).toBeDisabled();
    } finally { view?.unmount(); vi.useRealTimers(); }
  });
  it("shows saved API trust without editable metadata or test tokens until edit is requested", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json({ data: [row], key_session: true }));
    show(); await screen.findByRole("button", { name: "Edit API issuer settings" });
    expect(screen.queryByRole("form")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Resource access token for test")).not.toBeInTheDocument();
    expect(screen.getByText("gateway-resource", { selector: "dd" })).toBeInTheDocument();
    expect(screen.getByText(/Saved draft available; test before activation/)).toBeInTheDocument();
  });
  it("discards unsaved test tokens on cancel and restores keyboard focus", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json({ data: [row], key_session: true }));
    show(); const opener = await screen.findByRole("button", { name: "Edit API issuer settings" });
    await userEvent.click(opener);
    await userEvent.type(screen.getByLabelText("Resource access token for test"), "synthetic-unsaved-token");
    await userEvent.keyboard("{Escape}");
    expect(screen.getByText("Discard unsaved changes?")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Discard changes" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await waitFor(() => expect(opener).toHaveFocus());
    await userEvent.click(opener);
    expect(screen.getByLabelText("Resource access token for test")).toHaveValue("");
  });
  it("saves a CAS draft, closes the dialog and updates the saved summary without activation", async () => {
    const calls: { path: string; body: Record<string, unknown> }[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (path, options) => {
      if (!options?.method) return json({ data: [row], key_session: true });
      calls.push({ path: String(path), body: JSON.parse(String(options.body)) });
      return json({ ...row, revision: 2, draft: { ...row.draft, jwks_url: "https://api-idp.example/updated-jwks" } });
    });
    show(); await userEvent.click(await screen.findByRole("button", { name: "Edit API issuer settings" }));
    await userEvent.clear(screen.getByLabelText("API JWKS URL"));
    await userEvent.type(screen.getByLabelText("API JWKS URL"), "https://api-idp.example/updated-jwks");
    await userEvent.click(screen.getByRole("button", { name: "Save API issuer draft" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByText("https://api-idp.example/updated-jwks")).toBeInTheDocument();
    expect(calls).toHaveLength(1);
    expect(calls[0].path).toBe("/admin/v1/api-issuers/tenant-a");
    expect(calls[0].body.expected_revision).toBe(1);
    expect(calls[0].body).not.toHaveProperty("issuer");
    expect(calls[0].body).not.toHaveProperty("audience");
    expect(calls[0].body).not.toHaveProperty("organization_id");
  });
  it("creates an API issuer in a separate dialog with tenant-safe mappings", async () => {
    let body: Record<string, unknown> | undefined;
    vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => {
      if (!options?.method) return json({ data: [], key_session: true });
      body = JSON.parse(String(options.body)); return json(row);
    });
    show(); await userEvent.click(await screen.findByRole("button", { name: "New API issuer" }));
    expect(screen.getByRole("dialog", { name: "New API issuer" })).toBeInTheDocument();
    for (const [label, value] of [["API issuer ID", "tenant-a"], ["API issuer name", "Company A API"], ["API organization ID", "org-a"], ["API issuer URL", row.issuer], ["API resource audience", row.audience], ["API JWKS URL", config.jwks_url]]) await userEvent.type(screen.getByLabelText(label), value);
    expect(screen.queryByRole("option", { name: "admin" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Save API issuer draft" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(body?.organization_id).toBe("org-a");
    expect(body?.role_mappings).toEqual({ owners: "org_admin", users: "user" });
  });

});
