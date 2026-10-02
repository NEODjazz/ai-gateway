import { useEffect, type PropsWithChildren } from "react";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider, useAuth } from "../auth/AuthContext";
import { OrganizationKeysPage } from "./OrganizationKeysPage";
import { OrganizationMembers } from "./OrganizationMembers";
import { LogsPage } from "./LogsPage";
import { App } from "../app/App";

const orgSession = { user_id: "operator", organization_id: "org-a", roles: ["org_admin"], capabilities: ["api_docs", "inference"], organization_capabilities: ["organization_reports", "organization_keys"], allowed_models: [], allowed_tools: [] };
const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
function Gate({ children }: PropsWithChildren) { const { session, restoreSession } = useAuth(); useEffect(() => { void restoreSession(); }, [restoreSession]); return session ? children : null; }
function show(child: React.ReactNode, path = "/") { sessionStorage.setItem("ai-gateway.admin-token", "fixture"); return render(<MemoryRouter initialEntries={[path]}><AuthProvider><Gate>{child}</Gate></AuthProvider></MemoryRouter>); }

describe("Organization console", () => {
  it("offers scoped navigation without platform administration", async () => {
    history.replaceState({}, "", "/ui/api-keys"); sessionStorage.setItem("ai-gateway.admin-token", "fixture");
    vi.spyOn(globalThis, "fetch").mockImplementation(async (url) => String(url).endsWith("/session") ? json(orgSession) : String(url).includes("/keys?") ? json({ data: [], total: 0 }) : json({ enabled: false }));
    render(<AuthProvider><App /></AuthProvider>);
    const nav = await screen.findByRole("navigation", { name: "Dashboard" });
    for (const name of ["Usage & spend", "Logs", "Virtual keys"]) expect(within(nav).getByRole("link", { name })).toBeInTheDocument();
    for (const name of ["Providers", "Credentials", "Models", "Organizations", "Settings", "Budgets"]) expect(within(nav).queryByRole("link", { name })).not.toBeInTheDocument();
    expect(await screen.findByText(/Read-only key inventory/)).toHaveTextContent("org-a");
    expect(screen.queryByRole("button", { name: "Create Key" })).not.toBeInTheDocument();
  });
  it("uses actual organization key totals and never requests global financials or directory", async () => {
    const paths: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (url) => { const path=String(url); paths.push(path); return path.endsWith("/session") ? json(orgSession) : json({ data: [{ id: path.includes("offset=25") ? "key-26" : "key-1", organization_id: "org-a" }], total: 60 }); });
    show(<OrganizationKeysPage />);
    expect(await screen.findByText("1–1 of 60")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(await screen.findByText("key-26")).toBeInTheDocument();
    expect(paths.some((path) => path.includes("organization_id=org-a") && path.includes("offset=25"))).toBe(true);
    expect(paths.some((path) => /financials|organizations|users|teams|budgets/.test(path))).toBe(false);
  });
  it("refuses key metadata from another tenant", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (url) => String(url).endsWith("/session") ? json(orgSession) : json({ data: [{ id: "foreign-key", organization_id: "org-b" }], total: 1 }));
    show(<OrganizationKeysPage />); expect(await screen.findByText("Key inventory does not match this organization.")).toBeInTheDocument();
    expect(screen.queryByText("foreign-key")).not.toBeInTheDocument();
  });
  it("ignores the global audit tab and pins log filters to the verified organization", async () => {
    const paths: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (url) => { const path=String(url); paths.push(path); return path.endsWith("/session") ? json(orgSession) : json({ data: [] }); });
    show(<LogsPage />, "/logs?tab=audit&organization_id=org-b");
    await screen.findByText(/Every request, session, trace/);
    await waitFor(() => expect(paths.some((path) => path.includes("request-logs?") && path.includes("organization_id=org-a"))).toBe(true));
    expect(screen.queryByRole("tab", { name: "Audit Logs" })).not.toBeInTheDocument();
    expect(paths.some((path) => /audit\/|organizations\?|users\?|teams\?/.test(path))).toBe(false);
    await userEvent.click(screen.getByRole("button", { name: "Filter" }));
    expect(screen.getByLabelText("Organization")).toBeDisabled(); expect(screen.getByLabelText("Organization")).toHaveValue("org-a");
  });
  it("saves explicit organization role approvals and supports membership paging", async () => {
    const requests: { path: string; body?: unknown }[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (url, options) => {
      const path=String(url); requests.push({ path, body: options?.body ? JSON.parse(String(options.body)) : undefined });
      return path.endsWith("/session") ? json({ ...orgSession, roles: ["admin"], capabilities: ["admin"] }) : json({ data: [{ organization_id: "org-a", user_id: "member-a", roles: ["user"], status: "active" }], total: 70 });
    });
    show(<OrganizationMembers organization="org-a" />);
    expect(await screen.findByText("1–1 of 70")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Next members" }));
    await waitFor(() => expect(requests.some((r) => r.path.endsWith("offset=25"))).toBe(true));
    await userEvent.click(screen.getByRole("button", { name: "Edit approval for member-a" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "org_admin" }));
    await userEvent.click(screen.getByRole("button", { name: "Save organization approval" }));
    await waitFor(() => expect(requests.find((r) => r.body)?.body).toEqual({ organization_id: "org-a", user_id: "member-a", roles: ["user", "org_admin"], status: "active" }));
    expect(screen.queryByRole("checkbox", { name: /^admin$/ })).not.toBeInTheDocument();
  });
});
