import { appRoutes, routeCapability } from "./routes";

describe("dashboard route manifest", () => {
  it("contains exactly 36 navigable route-based pages", () => expect(appRoutes.filter((route) => route.navigation !== false)).toHaveLength(36));
  it("uses unique absolute paths", () => {
    const paths = appRoutes.map((route) => route.path);
    expect(new Set(paths).size).toBe(paths.length);
    expect(paths.every((path) => path.startsWith("/"))).toBe(true);
  });
  it("keeps unsupported capabilities explicit", () => {
    const unavailable = appRoutes.filter((route) => !route.available).map((route) => route.path);
    expect(unavailable).not.toContain("/search-tools");
    expect(unavailable).not.toContain("/skills");
    expect(unavailable).not.toContain("/tag-management");
    expect(unavailable).not.toContain("/policies");
    expect(unavailable).not.toContain("/router-settings");
  });
  it("covers every navigation group", () => {
    expect(new Set(appRoutes.map((route) => route.group))).toEqual(new Set(["Monitor", "Manage", "Access Control", "AI Hub", "Govern", "System"]));
  });
  it("keeps identity resources in Access Control", () => {
    expect(appRoutes.filter((route) => route.group === "Access Control" && route.navigation !== false).map((route) => route.title)).toEqual(["Organizations", "Teams", "Users", "Access groups", "Projects"]);
    expect(appRoutes).toEqual(expect.arrayContaining([
      expect.objectContaining({ path: "/access-groups/:id", navigation: false }),
      expect.objectContaining({ path: "/projects/:id", navigation: false })
    ]));
  });
  it("provides a client-side details route for every inspect destination", () => {
    expect(appRoutes.map((route) => route.path)).toEqual(expect.arrayContaining([
      "/api-keys/:id",
      "/organizations/:id",
      "/teams/:id",
      "/access-groups/:id",
      "/projects/:id",
      "/budgets/:id"
    ]));
  });
  it("combines request and audit logs under Monitor", () => {
    expect(appRoutes.find((route) => route.path === "/logs")).toMatchObject({ title: "Logs", group: "Monitor", available: true });
    expect(appRoutes.some((route) => route.path === "/audit" || route.path === "/request-logs")).toBe(false);
  });
  it("limits non-admin navigation to explicitly supported capabilities", () => {
    expect(routeCapability(appRoutes.find((route) => route.path === "/providers")!)).toBe("admin");
    expect(routeCapability(appRoutes.find((route) => route.path === "/teams")!)).toBe("team_directory");
    expect(routeCapability(appRoutes.find((route) => route.path === "/playground")!)).toBe("inference");
    expect(routeCapability(appRoutes.find((route) => route.path === "/skills")!)).toBe("inference");
    expect(routeCapability(appRoutes.find((route) => route.path === "/search-tools")!)).toBe("inference");
    expect(routeCapability(appRoutes.find((route) => route.path === "/api-reference")!)).toBe("api_docs");
  });
});
