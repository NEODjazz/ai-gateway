import { appRoutes } from "./routes";
import { navigationGroups, navigationIcon } from "./navigation";

describe("dashboard navigation", () => {
  it("keeps the requested section order without collapsible group items", () => {
    expect(navigationGroups).toEqual(["Manage", "Monitor", "Access Control", "AI Hub", "Govern", "System"]);
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
