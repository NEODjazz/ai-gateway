import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider, useAuth } from "./AuthContext";

function Consumer() {
  const { token, session, signIn, signOut } = useAuth();
  return <><span>{token || "signed-out"}</span><span>{session?.user_id || "no-session"}</span><button onClick={() => void signIn("Bearer abc").catch(() => undefined)}>Sign in</button><button onClick={signOut}>Sign out</button></>;
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
    sessionStorage.setItem("ai-gateway.admin-token", "restored");
    render(<AuthProvider><Consumer /></AuthProvider>);
    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));
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
});
