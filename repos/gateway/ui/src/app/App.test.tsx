import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { App } from "./App";

describe("App", () => {
  it("shows the token gate without authentication", () => {
    history.replaceState({}, "", "/ui/");
    render(<AuthProvider><App /></AuthProvider>);
    expect(screen.getByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.getByLabelText("Admin bearer token")).toHaveAttribute("type", "password");
  });

  it("opens route-based navigation after sign in", async () => {
    history.replaceState({}, "", "/ui/overview");
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ data: [] }), { status: 200 }));
    render(<AuthProvider><App /></AuthProvider>);
    await userEvent.type(screen.getByLabelText("Admin bearer token"), "token");
    await userEvent.click(screen.getByRole("button", { name: "Open console" }));
    expect(await screen.findByRole("navigation", { name: "Dashboard" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Providers" })).toHaveAttribute("href", "/ui/providers");
  });

  it("signs out from the shared layout", async () => {
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    history.replaceState({}, "", "/ui/settings");
    render(<AuthProvider><App /></AuthProvider>);
    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));
    expect(screen.getByRole("heading", { name: "Sign in" })).toBeInTheDocument();
  });
});
