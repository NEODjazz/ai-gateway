import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider, useAuth } from "./AuthContext";

function Consumer() {
  const { token, signIn, signOut } = useAuth();
  return <><span>{token || "signed-out"}</span><button onClick={() => signIn("Bearer abc")}>Sign in</button><button onClick={signOut}>Sign out</button></>;
}

describe("AuthProvider", () => {
  it("normalizes and stores a bearer token", async () => {
    render(<AuthProvider><Consumer /></AuthProvider>);
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(screen.getByText("abc")).toBeInTheDocument();
    expect(sessionStorage.getItem("ai-gateway.admin-token")).toBe("abc");
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
});
