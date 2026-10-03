import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation, useNavigate } from "react-router-dom";
import { SidebarNavigation } from "./SidebarNavigation";
import { visibleNavigationSections } from "./navigation";
import { appRoutes } from "./routes";
import { sidebarStorageKey } from "./sidebarState";
import { GravityThemeScope } from "../components/GravityThemeScope";

function Harness({ compact = false }: { compact?: boolean }) {
  const location = useLocation();
  const navigate = useNavigate();
  return <><SidebarNavigation sections={visibleNavigationSections(appRoutes)} compact={compact} /><output aria-label="Current route">{location.pathname}</output><button onClick={() => navigate("/providers")}>Go to providers</button><button onClick={() => navigate("/usage")}>Go to usage</button></>;
}
function show(path = "/overview", compact = false) {
  return render(<GravityThemeScope><MemoryRouter initialEntries={[path]}><Harness compact={compact} /></MemoryRouter></GravityThemeScope>);
}

describe("grouped sidebar", () => {
  it("renders section headings and accessible disclosure controls", () => {
    show();
    expect(screen.getAllByRole("heading", { level: 2 }).map((heading) => heading.textContent)).toEqual(["AI Gateway", "Observability", "Access Control", "Developer Tools", "Settings"]);
    expect(screen.getByRole("button", { name: "Models & endpoints" })).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("link", { name: "Providers" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Overview" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Response cache" })).toHaveAttribute("href", "/cache");
  });
  it("opens a stored collapsed group on a direct URL and highlights the exact child", () => {
    window.localStorage.setItem(sidebarStorageKey, JSON.stringify({ models: true }));
    show("/providers");
    expect(screen.getByRole("button", { name: "Models & endpoints" })).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("link", { name: "Providers" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Models" })).not.toHaveAttribute("aria-current");
  });
  it("opens the active group for case-insensitive URLs supported by the router", () => {
    show("/Providers");
    expect(screen.getByRole("button", { name: "Models & endpoints" })).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("link", { name: "Providers" })).toHaveAttribute("aria-current", "page");
  });
  it("keeps highlighting detail routes and preserves ordinary browser link behavior", async () => {
    show("/api-keys/key-a");
    const link = screen.getByRole("link", { name: "Virtual keys" });
    expect(link).toHaveAttribute("aria-current", "page");
    await userEvent.click(link);
    expect(screen.getByLabelText("Current route")).toHaveTextContent("/api-keys");
    // NavLink retains the native href for opening links in another tab.
    expect(link).toHaveAttribute("href", "/api-keys");
  });
  it("stores expanded groups and restores them after remounting", async () => {
    const view = show();
    await userEvent.click(screen.getByRole("button", { name: "MCP" }));
    expect(screen.getByRole("link", { name: "MCP servers" })).toBeInTheDocument();
    view.unmount(); show();
    expect(screen.getByRole("button", { name: "MCP" })).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("link", { name: "MCP toolsets" })).toBeInTheDocument();
  });
  it("allows deliberate collapse and reopens the group on the next route transition", async () => {
    show("/providers");
    await userEvent.click(screen.getByRole("button", { name: "Models & endpoints" }));
    expect(screen.queryByRole("link", { name: "Providers" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Go to usage" }));
    expect(screen.getByRole("button", { name: "Models & endpoints" })).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(screen.getByRole("button", { name: "Go to providers" }));
    expect(screen.getByRole("link", { name: "Providers" })).toHaveAttribute("aria-current", "page");
  });
  it("supports keyboard disclosure, child navigation and return to the parent", async () => {
    show(); const group = screen.getByRole("button", { name: "Models & endpoints" });
    group.focus(); await userEvent.keyboard("{Enter}");
    expect(group).toHaveAttribute("aria-expanded", "true");
    await userEvent.tab(); expect(screen.getByRole("link", { name: "Models" })).toHaveFocus();
    await userEvent.keyboard("{End}"); expect(screen.getByRole("link", { name: "Model groups" })).toHaveFocus();
    await userEvent.keyboard("{Home}{ArrowDown}"); expect(screen.getByRole("link", { name: "Model onboarding" })).toHaveFocus();
    await userEvent.keyboard("{ArrowLeft}"); expect(group).toHaveFocus(); expect(group).toHaveAttribute("aria-expanded", "false");
    await userEvent.keyboard(" "); expect(group).toHaveAttribute("aria-expanded", "true");
    await userEvent.keyboard("{ArrowLeft}{ArrowRight}");
    expect(screen.getByRole("link", { name: "Models" })).toHaveFocus();
  });
  it("opens compact groups by keyboard and restores focus after Escape", async () => {
    show("/overview", true);
    const group = screen.getByRole("button", { name: "Models & endpoints" });
    group.focus(); await userEvent.keyboard("{ArrowRight}");
    const popup = await screen.findByRole("dialog", { name: "Models & endpoints navigation" });
    expect(group).toHaveAttribute("aria-expanded", "true");
    const first = within(popup).getByRole("link", { name: "Models" });
    await waitFor(() => expect(first).toHaveFocus());
    await userEvent.keyboard("{ArrowDown}"); expect(within(popup).getByRole("link", { name: "Model onboarding" })).toHaveFocus();
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(group).toHaveFocus(); expect(group).toHaveAttribute("aria-expanded", "false");
  });
  it("closes compact popups after navigation and retains expanded-mode preferences", async () => {
    window.localStorage.setItem(sidebarStorageKey, JSON.stringify({ models: true, mcp: false }));
    show("/overview", true);
    await userEvent.click(screen.getByRole("button", { name: "Models & endpoints" }));
    const popup = await screen.findByRole("dialog");
    await userEvent.click(within(popup).getByRole("link", { name: "Providers" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByLabelText("Current route")).toHaveTextContent("/providers");
    expect(JSON.parse(window.localStorage.getItem(sidebarStorageKey)!).mcp).toBe(false);
  });
});
