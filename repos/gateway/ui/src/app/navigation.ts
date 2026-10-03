import {
  AbbrApi,
  Bell,
  BookOpen,
  Boxes3,
  Briefcase,
  ChartLine,
  CircleDollar,
  Cloud,
  Cubes3,
  Database,
  FaceRobot,
  FileText,
  Folder,
  Gear,
  GearBranches,
  House,
  Key,
  Layers3Diagonal,
  ListCheckLock,
  Magnifier,
  Person,
  PersonMagnifier,
  Persons,
  PersonsLock,
  Play,
  PlugConnection,
  Pulse,
  Rocket,
  Route,
  ScalesBalanced,
  Server,
  Shield,
  ShieldKeyhole,
  Sparkles,
  Tag,
  TagDollar,
} from "@gravity-ui/icons";
import type { AsideHeaderItem } from "@gravity-ui/navigation/build/esm/index.js";
import type { AppRoute } from "./routes";

type NavigationGroup = { id: string; title: string; paths: string[] };
type NavigationSection = { id: string; title: string; items: (string | NavigationGroup)[] };

export const navigationSections: NavigationSection[] = [
  { id: "gateway", title: "AI Gateway", items: ["/api-keys", "/playground",
    { id: "models", title: "Models & endpoints", paths: ["/models", "/model-onboarding", "/providers", "/credentials", "/deployments", "/model-groups"] },
    { id: "agentic", title: "Agentic", paths: ["/agents"] },
    { id: "mcp", title: "MCP", paths: ["/mcp-servers", "/mcp-toolsets"] },
    "/skills", { id: "tools", title: "Tools", paths: ["/search-tools", "/tool-policies"] }, "/guardrails", "/policies"] },
  { id: "observability", title: "Observability", items: ["/overview", "/usage", "/customers", "/cost-optimization", "/logs", "/routing", "/guardrails-monitor"] },
  { id: "access", title: "Access Control", items: ["/organizations", "/teams", "/users", "/access-groups", "/projects", "/budgets"] },
  { id: "developer", title: "Developer Tools", items: ["/api-reference", "/ai-hub", "/cache", "/tag-management"] },
  { id: "settings", title: "Settings", items: [{ id: "settings", title: "Settings", paths: ["/router-settings", "/logging", "/settings"] }] },
];

export type NavigationLink = { kind: "link"; route: AppRoute; title: string };
export type VisibleNavigationGroup = NavigationGroup & { kind: "group"; links: NavigationLink[] };
export type VisibleNavigationSection = { id: string; title: string; items: (NavigationLink | VisibleNavigationGroup)[] };

const navigationTitles: Record<string, string> = { "/cache": "Response cache", "/settings": "Single sign-on" };

// The caller supplies routes filtered by the same capabilities as the router.
export function visibleNavigationSections(routes: AppRoute[]): VisibleNavigationSection[] {
  const byPath = new Map(routes.filter((route) => route.navigation !== false).map((route) => [route.path, route]));
  const link = (path: string): NavigationLink[] => {
    const route = byPath.get(path);
    return route ? [{ kind: "link", route, title: navigationTitles[path] || route.title }] : [];
  };
  return navigationSections.flatMap((section) => {
    const items = section.items.flatMap<NavigationLink | VisibleNavigationGroup>((item) => {
      if (typeof item === "string") return link(item);
      const links = item.paths.flatMap(link);
      return links.length ? [{ ...item, kind: "group", links }] : [];
    });
    return items.length ? [{ id: section.id, title: section.title, items }] : [];
  });
}

export function isCurrentNavigationRoute(path: string, pathname: string): boolean {
  return pathname === path || pathname.startsWith(`${path}/`);
}

export function activeNavigationGroup(sections: VisibleNavigationSection[], pathname: string): string | undefined {
  for (const section of sections) {
    for (const item of section.items) {
      if (item.kind === "group" && item.links.some((link) => isCurrentNavigationRoute(link.route.path, pathname))) return item.id;
    }
  }
}

const routeIcons: Record<string, NonNullable<AsideHeaderItem["icon"]>> = {
  "/api-keys": Key,
  "/models": Cubes3,
  "/model-onboarding": Rocket,
  "/providers": Cloud,
  "/credentials": ShieldKeyhole,
  "/deployments": Server,
  "/model-groups": Layers3Diagonal,
  "/overview": House,
  "/usage": ChartLine,
  "/customers": PersonMagnifier,
  "/logs": FileText,
  "/routing": Route,
  "/playground": Play,
  "/organizations": Briefcase,
  "/teams": Persons,
  "/users": Person,
  "/access-groups": PersonsLock,
  "/projects": Folder,
  "/ai-hub": Sparkles,
  "/cost-optimization": TagDollar,
  "/mcp-servers": PlugConnection,
  "/mcp-toolsets": Boxes3,
  "/tool-policies": ListCheckLock,
  "/agents": FaceRobot,
  "/search-tools": Magnifier,
  "/skills": BookOpen,
  "/guardrails": Shield,
  "/guardrails-monitor": Pulse,
  "/budgets": CircleDollar,
  "/policies": ScalesBalanced,
  "/tag-management": Tag,
  "/cache": Database,
  "/logging": Bell,
  "/router-settings": GearBranches,
  "/api-reference": AbbrApi,
  "/settings": Gear,
};

export function navigationIcon(path: string): NonNullable<AsideHeaderItem["icon"]> {
  return routeIcons[path] || Sparkles;
}
