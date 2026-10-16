import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider, useAuth } from "./AuthContext";

function Consumer() {
  const { token, session, signIn, signOut, signingOut, signOutError, prepareSSOSignIn } = useAuth();
  return <><span>{token || "signed-out"}</span><span>{session?.user_id || "no-session"}</span><button onClick={() => void signIn("Bearer abc").catch(() => undefined)}>Sign in</button><button onClick={prepareSSOSignIn}>Start fresh SSO</button><button disabled={signingOut} onClick={() => void signOut()}>Sign out</button>{signOutError && <p role="alert">{signOutError}</p>}</>;
}

const session = { user_id: "admin-user", roles: ["admin"], allowed_models: ["gpt"], allowed_tools: [], capabilities: ["admin", "api_docs", "inference", "team_directory"] };

describe("AuthProvider", () => {
  it("normalizes and stores a bearer token", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify(session), { status: 200 }));
    render(<AuthProvider><Consumer /></AuthProvider>);
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByText("abc")).toBeInTheDocument();
    expect(screen.getByText("admin-user")).toBeInTheDocument();
    expect(sessionStorage.getItem("ai-gateway.admin-token")).toBe("abc");
    expect(fetchMock.mock.calls[0][0]).toBe("/admin/v1/session");
    expect(new Headers(fetchMock.mock.calls[0][1]?.headers).get("Authorization")).toBe("Bearer abc");
  });

  it("restores a token from tab storage", () => {
    sessionStorage.setItem("ai-gateway.admin-token", "restored");
    render(<AuthProvider><Consumer /></AuthProvider>);
    expect(screen.getByText("restored")).toBeInTheDocument();
  });

  it("clears token state on sign out", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(null, { status: 204 }));
    sessionStorage.setItem("ai-gateway.admin-token", "restored");
    render(<AuthProvider><Consumer /></AuthProvider>);
    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));
    expect(await screen.findByText("signed-out")).toBeInTheDocument();
    expect(sessionStorage.getItem("ai-gateway.admin-token")).toBeNull();
  });

  it("clears the old bearer identity before switching to a fresh OIDC connection", async () => {
    sessionStorage.setItem("ai-gateway.admin-token", "old-api-token");
    render(<AuthProvider><Consumer /></AuthProvider>);
    await userEvent.click(screen.getByRole("button", { name: "Start fresh SSO" }));
    expect(screen.getByText("signed-out")).toBeInTheDocument();
    expect(sessionStorage.getItem("ai-gateway.admin-token")).toBeNull();
  });

  it("does not store a credential that fails validation", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ error: { code: "unauthorized", message: "Invalid credential" } }), { status: 401 }));
    render(<AuthProvider><Consumer /></AuthProvider>);
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path]) => String(path) === "/admin/v1/session")).toBe(true));
    expect(screen.getByText("signed-out")).toBeInTheDocument();
    expect(sessionStorage.getItem("ai-gateway.admin-token")).toBeNull();
  });
  it.each(["unavailable", "network"])("retains the session until server logout succeeds after %s failure", async (failure) => {
    sessionStorage.setItem("ai-gateway.admin-token", "browser-sso");
    const fetchMock = vi.spyOn(globalThis, "fetch");
    if (failure === "network") fetchMock.mockRejectedValueOnce(new TypeError("Network error"));
    else fetchMock.mockResolvedValueOnce(new Response(null, { status: 503 }));
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    render(<AuthProvider><Consumer /></AuthProvider>);
    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("could not be confirmed");
    expect(sessionStorage.getItem("ai-gateway.admin-token")).toBe("browser-sso");
    expect(screen.getByText("browser-sso")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));
    expect(await screen.findByText("signed-out")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("waits for logout and does not clear a newer signed-in identity", async () => {
    sessionStorage.setItem("ai-gateway.admin-token", "browser-sso");
    let finishLogout!: (response: Response) => void;
    const pending = new Promise<Response>((resolve) => { finishLogout = resolve; });
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => String(input) === "/auth/sso/logout" ? pending : new Response(JSON.stringify(session)));
    render(<AuthProvider><Consumer /></AuthProvider>);
    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));
    expect(screen.getByRole("button", { name: "Sign out" })).toBeDisabled();
    expect(screen.getByText("browser-sso")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByText("abc")).toBeInTheDocument();
    finishLogout(new Response(null, { status: 204 }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Sign out" })).toBeEnabled());
    expect(sessionStorage.getItem("ai-gateway.admin-token")).toBe("abc");
  });

});
