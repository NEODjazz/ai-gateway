import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { App } from "./App";

const adminSession = { user_id: "admin-user", roles: ["admin"], allowed_models: ["gpt"], allowed_tools: [], capabilities: ["admin", "api_docs", "inference", "team_directory"] };
const teamSession = { user_id: "team-user", team_id: "team-a", roles: ["team_admin"], allowed_models: ["gpt"], allowed_tools: [], capabilities: ["api_docs", "inference", "team_directory"] };

function mockConsole(session = adminSession) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    if (String(input) === "/admin/v1/session") return new Response(JSON.stringify(session), { status: 200 });
    return new Response(JSON.stringify({ data: [] }), { status: 200 });
  });
}

describe("App", () => {
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
    expect(within(navigation).queryByRole("button", { name: /Manage|Monitor|Access Control|AI Hub|Govern|System/ })).not.toBeInTheDocument();
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

  it("signs out from the shared layout", async () => {
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    history.replaceState({}, "", "/ui/settings");
    mockConsole();
    render(<AuthProvider><App /></AuthProvider>);
    await userEvent.click(await screen.findByRole("button", { name: "Sign out" }));
    expect(screen.getByRole("heading", { name: "Sign in" })).toBeInTheDocument();
  });
});
