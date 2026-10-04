import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { App } from "./App";
import { GravityThemeScope } from "../components/GravityThemeScope";

const adminSession = { user_id: "admin-user", roles: ["admin"], allowed_models: ["gpt"], allowed_tools: [], capabilities: ["admin", "api_docs", "inference", "team_directory"] };
const teamSession = { user_id: "team-user", team_id: "team-a", roles: ["team_admin"], allowed_models: ["gpt"], allowed_tools: [], capabilities: ["api_docs", "inference", "team_directory"] };

function mockConsole(session = adminSession) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    if (String(input) === "/admin/v1/session") return new Response(JSON.stringify(session), { status: 200 });
    return new Response(JSON.stringify({ data: [] }), { status: 200 });
  });
}

describe("App", () => {
  it("uses compact navigation on narrow screens, restores desktop choice and cleans up its breakpoint listener", async () => {
    let change: (event: MediaQueryListEvent) => void = () => {};
    const add = vi.fn((_type: string, listener: EventListenerOrEventListenerObject) => { change = listener as (event: MediaQueryListEvent) => void; });
    const remove = vi.fn();
    const original = window.matchMedia;
    vi.spyOn(window, "matchMedia").mockImplementation((query) => query === "(max-width: 767px)"
      ? { ...original(query), matches: true, addEventListener: add, removeEventListener: remove } : original(query));
    history.replaceState({}, "", "/ui/overview"); sessionStorage.setItem("ai-gateway.admin-token", "test-token"); mockConsole();
    const view = render(<GravityThemeScope><AuthProvider><App /></AuthProvider></GravityThemeScope>);
    const navigation = await screen.findByRole("navigation", { name: "Dashboard" });
    expect(screen.queryByRole("button", { name: "Expand navigation" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Collapse navigation" })).not.toBeInTheDocument();
    await userEvent.click(within(navigation).getByRole("button", { name: "Models & endpoints" }));
    expect(await screen.findByRole("dialog", { name: "Models & endpoints navigation" })).toContainElement(screen.getByRole("link", { name: "Providers" }));
    act(() => change({ matches: false } as MediaQueryListEvent));
    expect(screen.queryByRole("dialog", { name: "Models & endpoints navigation" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Collapse navigation" }));
    expect(screen.getByRole("button", { name: "Expand navigation" })).toBeInTheDocument();
    act(() => change({ matches: true } as MediaQueryListEvent));
    expect(screen.queryByRole("button", { name: "Expand navigation" })).not.toBeInTheDocument();
    act(() => change({ matches: false } as MediaQueryListEvent));
    expect(screen.getByRole("button", { name: "Expand navigation" })).toBeInTheDocument();
    view.unmount(); expect(add).toHaveBeenCalledWith("change", change); expect(remove).toHaveBeenCalledWith("change", change);
  });
  it("shows the token gate without authentication", async () => {
    history.replaceState({}, "", "/ui/");
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ enabled: false, start_url: "/auth/sso/start" }), { status: 200 }));
    render(<AuthProvider><App /></AuthProvider>);
    await screen.findByRole("heading", { name: "Sign in" });
    expect(screen.getByLabelText("Gateway bearer token")).toHaveAttribute("type", "password");
  });

  it("opens route-based navigation after sign in", async () => {
    history.replaceState({}, "", "/ui/overview");
    mockConsole();
    render(<AuthProvider><App /></AuthProvider>);
    await userEvent.type(await screen.findByLabelText("Gateway bearer token"), "token");
    await userEvent.click(screen.getByRole("button", { name: "Open console" }));
    const navigation = await screen.findByRole("navigation", { name: "Dashboard" });
    expect(within(navigation).getByRole("heading", { name: "Observability" })).toBeInTheDocument();
    await userEvent.click(within(navigation).getByRole("button", { name: "Models & endpoints" }));
    expect(screen.getByRole("link", { name: "Providers" })).toHaveAttribute("href", "/ui/providers");
    expect(screen.getByRole("link", { name: "Logs" })).toHaveAttribute("href", "/ui/logs");
    expect(within(navigation).getByRole("link", { name: "Organizations" })).toBeInTheDocument();
    expect(screen.getByText("admin-user")).toBeInTheDocument();
  });

  it("shows only scoped pages for a team administrator", async () => {
    history.replaceState({}, "", "/ui/providers");
    mockConsole(teamSession);
    render(<AuthProvider><App /></AuthProvider>);
    await userEvent.type(await screen.findByLabelText("Gateway bearer token"), "team-token");
    await userEvent.click(screen.getByRole("button", { name: "Open console" }));
    const navigation = await screen.findByRole("navigation", { name: "Dashboard" });
    expect(within(navigation).getByRole("link", { name: "Playground" })).toBeInTheDocument();
    expect(within(navigation).getByRole("link", { name: "Teams" })).toBeInTheDocument();
    expect(within(navigation).getByRole("link", { name: "Users" })).toBeInTheDocument();
    expect(within(navigation).queryByRole("link", { name: "Providers" })).not.toBeInTheDocument();
    expect(within(navigation).queryByRole("link", { name: "Virtual keys" })).not.toBeInTheDocument();
    expect(within(navigation).queryByRole("button", { name: "Models & endpoints" })).not.toBeInTheDocument();
    expect(within(navigation).queryByRole("heading", { name: "Settings" })).not.toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Playground" })).toBeInTheDocument();
  });

  it("rejects an invalid credential before opening the console", async () => {
    history.replaceState({}, "", "/ui/");
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => String(input) === "/auth/sso/config"
      ? new Response(JSON.stringify({ enabled: false, start_url: "/auth/sso/start" }), { status: 200 })
      : new Response(JSON.stringify({ error: { code: "unauthorized", message: "Invalid credential" } }), { status: 401 }));
    render(<AuthProvider><App /></AuthProvider>);
    await userEvent.type(await screen.findByLabelText("Gateway bearer token"), "bad-token");
    await userEvent.click(screen.getByRole("button", { name: "Open console" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Invalid credential");
    expect(screen.queryByRole("navigation", { name: "Dashboard" })).not.toBeInTheDocument();
    expect(sessionStorage.getItem("ai-gateway.admin-token")).toBeNull();
  });

  it("restores an encrypted browser SSO session without exposing a token", async () => {
    history.replaceState({}, "", "/ui/");
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      if (String(input) === "/auth/sso/config") return new Response(JSON.stringify({ enabled: true, start_url: "/auth/sso/start" }), { status: 200 });
      if (String(input) === "/admin/v1/session") return new Response(JSON.stringify(adminSession), { status: 200 });
      return new Response(JSON.stringify({ data: [] }), { status: 200 });
    });
    render(<AuthProvider><App /></AuthProvider>);
    expect(await screen.findByRole("navigation", { name: "Dashboard" })).toBeInTheDocument();
    const sessionCall = fetchMock.mock.calls.find(([path]) => String(path) === "/admin/v1/session");
    expect(new Headers(sessionCall?.[1]?.headers).get("Authorization")).toBeNull();
    expect(sessionStorage.getItem("ai-gateway.admin-token")).toBe("browser-sso");
    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));
    expect(await screen.findByRole("link", { name: "Continue with SSO" })).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([path]) => String(path) === "/admin/v1/session")).toHaveLength(1);
    expect(fetchMock.mock.calls.some(([path, options]) => String(path) === "/auth/sso/logout" && options?.method === "POST")).toBe(true);
  });

  it("offers browser SSO when enabled and no session exists", async () => {
    history.replaceState({}, "", "/ui/");
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      if (String(input) === "/auth/sso/config") return new Response(JSON.stringify({ enabled: true, start_url: "/auth/sso/start" }), { status: 200 });
      if (String(input) === "/auth/sso/logout") return new Response(null, { status: 204 });
      return new Response(JSON.stringify({ error: { code: "unauthorized", message: "Invalid credential" } }), { status: 401 });
    });
    render(<AuthProvider><App /></AuthProvider>);
    expect(await screen.findByRole("link", { name: "Continue with SSO" })).toHaveAttribute("href", "/auth/sso/start");
    expect(screen.getByLabelText("Gateway bearer token")).toBeInTheDocument();
  });

  it("offers organization-bound connections and leaves authorization to a fresh OIDC sign-in", async () => {
    history.replaceState({}, "", "/ui/");
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => String(input) === "/auth/sso/config"
      ? new Response(JSON.stringify({ enabled: true, connections: [{ id: "tenant-a", name: "Company A", provider: "entra", organization_id: "org-a", start_url: "https://untrusted.example" }, { id: "default", name: "Platform", provider: "oidc" }] }), { status: 200 })
      : new Response(JSON.stringify({ error: { message: "No browser session" } }), { status: 401 }));
    render(<AuthProvider><App /></AuthProvider>);
    expect(await screen.findByRole("link", { name: "Continue with Company A · org-a" })).toHaveAttribute("href", "/auth/sso/start?connection=tenant-a");
    expect(screen.getByRole("link", { name: "Continue with Platform · Platform" })).toHaveAttribute("href", "/auth/sso/start?connection=default");
    expect(sessionStorage.getItem("ai-gateway.admin-token")).toBeNull();
  });

  it("signs out from the shared layout", async () => {
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    history.replaceState({}, "", "/ui/settings");
    mockConsole();
    render(<AuthProvider><App /></AuthProvider>);
    await userEvent.click(await screen.findByRole("button", { name: "Sign out" }));
    expect(screen.getByRole("heading", { name: "Sign in" })).toBeInTheDocument();
  });
});
