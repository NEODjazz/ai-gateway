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
    await userEvent.type(screen.getByLabelText("Resource access token for test"), "synthetic-access-token");
    await userEvent.click(screen.getByRole("button", { name: "Test API issuer" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Principal approval required");
    expect(screen.getByLabelText("Resource access token for test")).toHaveValue("");
    expect(screen.getByRole("button", { name: "Activate API issuer" })).toBeDisabled();
  });
  it("requires a recovery key and does not enable mutation after a failed inventory request", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(json({ data: [row], key_session: false })).mockResolvedValue(json({ error: { message: "Auth unavailable" } }, 503));
    show(); await userEvent.click(await screen.findByRole("button", { name: row.name }));
    expect(screen.getByRole("button", { name: "Save API issuer draft" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Reload API issuers" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Auth unavailable");
    expect(screen.queryByRole("button", { name: row.name })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New API issuer" })).toBeDisabled();
  });
  it("does not select an old issuer after a delayed mutation resolves", async () => {
    let finish!: (response: Response) => void;
    const delayed = new Promise<Response>((resolve) => { finish = resolve; });
    vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => options?.method ? delayed : json({ data: [row, { ...row, id: "tenant-b", name: "Company B API", organization_id: "org-b" }], key_session: true }));
    show(); await userEvent.click(await screen.findByRole("button", { name: row.name }));
    await userEvent.type(screen.getByLabelText("Resource access token for test"), "synthetic-token");
    await userEvent.click(screen.getByRole("button", { name: "Test API issuer" }));
    await userEvent.click(screen.getByRole("button", { name: "Company B API" }));
    await act(async () => { finish(json({ ...row, revision: 2 })); });
    expect(screen.getByLabelText("API issuer ID")).toHaveValue("tenant-b");
  });
  it("expires test proof visibly without another user action", async () => {
    vi.useFakeTimers(); let view: ReturnType<typeof show> | undefined;
    try {
      vi.spyOn(globalThis, "fetch").mockResolvedValue(json({ data: [{ ...row, last_test_status: "passed", test_expires_at: Math.floor(Date.now() / 1000) + 1 }], key_session: true }));
      await act(async () => { view = show(); });
      // Fire a direct click under fake timers; user-event waits on timer ticks.
      await act(async () => { screen.getByRole("button", { name: row.name }).click(); });
      expect(screen.getByRole("button", { name: "Activate API issuer" })).toBeEnabled();
      act(() => vi.advanceTimersByTime(1001));
      expect(screen.getByRole("button", { name: "Activate API issuer" })).toBeDisabled();
    } finally { view?.unmount(); vi.useRealTimers(); }
  });
});
