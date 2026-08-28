import { appRoutes } from "./routes";

describe("dashboard route manifest", () => {
  it("contains exactly 36 route-based pages", () => expect(appRoutes).toHaveLength(36));
  it("uses unique absolute paths", () => {
    const paths = appRoutes.map((route) => route.path);
    expect(new Set(paths).size).toBe(paths.length);
    expect(paths.every((path) => path.startsWith("/"))).toBe(true);
  });
  it("keeps unsupported capabilities explicit", () => {
    const unavailable = appRoutes.filter((route) => !route.available).map((route) => route.path);
    expect(unavailable).toEqual(expect.arrayContaining(["/policies", "/tag-management", "/skills"]));
    expect(unavailable).not.toContain("/router-settings");
  });
  it("covers every navigation group", () => {
    expect(new Set(appRoutes.map((route) => route.group))).toEqual(new Set(["Monitor", "Manage", "Access Control", "AI Hub", "Govern", "System"]));
  });
  it("keeps identity resources in Access Control", () => {
    expect(appRoutes.filter((route) => route.group === "Access Control").map((route) => route.title)).toEqual(["Organizations", "Teams", "Users", "Access groups", "Projects"]);
  });
  it("combines request and audit logs under Monitor", () => {
    expect(appRoutes.find((route) => route.path === "/logs")).toMatchObject({ title: "Logs", group: "Monitor", available: true });
    expect(appRoutes.some((route) => route.path === "/audit" || route.path === "/request-logs")).toBe(false);
  });
});
