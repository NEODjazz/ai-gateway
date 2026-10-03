import { appRoutes } from "./routes";
import { activeNavigationGroup, navigationSections, navigationIcon, visibleNavigationSections } from "./navigation";
import { canAccessRoute } from "./routes";
import type { AdminSession } from "../auth/AuthContext";

describe("dashboard navigation", () => {
  it("keeps the requested section and submenu order", () => {
    expect(navigationSections.map((section) => section.title)).toEqual(["AI Gateway", "Observability", "Access Control", "Developer Tools", "Settings"]);
    expect(navigationSections.flatMap((section) => section.items.flatMap((item) => typeof item === "string" ? [] : item.title))).toEqual(["Models & endpoints", "Agentic", "MCP", "Tools", "Settings"]);
  });

  it("includes every existing navigable route exactly once and excludes details routes", () => {
    const sections = visibleNavigationSections(appRoutes);
    const paths = sections.flatMap((section) => section.items.flatMap((item) => item.kind === "link" ? [item.route.path] : item.links.map((link) => link.route.path)));
    expect(paths).toHaveLength(36);
    expect(new Set(paths)).toEqual(new Set(appRoutes.filter((route) => route.navigation !== false).map((route) => route.path)));
  });

  it("places model administration and settings in their own groups without changing route titles", () => {
    const sections = visibleNavigationSections(appRoutes);
    const models = sections[0].items.find((item) => item.kind === "group" && item.id === "models");
    expect(models?.kind === "group" && models.links.map((link) => link.route.path)).toEqual(["/models", "/model-onboarding", "/providers", "/credentials", "/deployments", "/model-groups"]);
    const settings = sections[4].items[0];
    expect(settings.kind === "group" && settings.links.map((link) => link.title)).toEqual(["Router settings", "Logging & alerts", "Single sign-on"]);
    expect(appRoutes.find((route) => route.path === "/settings")?.title).toBe("Settings");
  });

  it.each([
    ["team_admin", ["team_directory", "inference", "api_docs"], ["AI Gateway", "Access Control", "Developer Tools"]],
    ["user", ["inference"], ["AI Gateway"]],
    ["org_admin", ["organization_reports", "organization_keys"], ["AI Gateway", "Observability"]],
  ])("filters groups and empty sections for %s using router capabilities", (role, capabilities, titles) => {
    const session = { roles: [role], capabilities, allowed_models: [], allowed_tools: [] } as AdminSession;
    const allowed = appRoutes.filter((route) => canAccessRoute(route, session));
    const sections = visibleNavigationSections(allowed);
    expect(sections.map((section) => section.title)).toEqual(titles);
    expect(sections.flatMap((section) => section.items).filter((item) => item.kind === "group").map((item) => item.id)).not.toContain("models");
  });

  it("recognizes exact routes and child routes without matching similar prefixes", () => {
    const sections = visibleNavigationSections(appRoutes);
    expect(activeNavigationGroup(sections, "/providers")).toBe("models");
    expect(activeNavigationGroup(sections, "/providers/provider-a")).toBe("models");
    expect(activeNavigationGroup(sections, "/Providers/provider-a")).toBe("models");
    expect(activeNavigationGroup(sections, "/providers-other")).toBeUndefined();
    expect(activeNavigationGroup(sections, "/usage")).toBeUndefined();
  });

  it("assigns a dedicated semantic icon to every navigable route", () => {
    const routes = appRoutes.filter((route) => route.navigation !== false);
    const icons = routes.map((route) => navigationIcon(route.path));

    expect(icons).toHaveLength(36);
    expect(new Set(icons).size).toBe(36);
    expect(navigationIcon("/api-keys")).not.toBe(navigationIcon("/models"));
    expect(navigationIcon("/providers")).not.toBe(navigationIcon("/deployments"));
    expect(navigationIcon("/logs")).not.toBe(navigationIcon("/usage"));
  });
});
