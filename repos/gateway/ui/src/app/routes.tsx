import { lazy, type ReactNode } from "react";
import { CapabilityPage } from "../components/CapabilityPage";
import { ResourcePage, type ResourceConfig } from "../components/ResourcePage";
import { resourceConfigs } from "../pages/resourceConfigs";
import { useAuth, type ConsoleCapability } from "../auth/AuthContext";

export type AppRoute = { path: string; title: string; group: "Monitor" | "Manage" | "Access Control" | "AI Hub" | "Govern" | "System"; element: ReactNode; available: boolean; capability?: ConsoleCapability; navigation?: boolean };

const OverviewPage = lazy(() => import("../pages/OverviewPage").then((module) => ({ default: module.OverviewPage })));
const UsagePage = lazy(() => import("../pages/UsagePage").then((module) => ({ default: module.UsagePage })));
const CustomerInsightsPage = lazy(() => import("../pages/CustomerInsightsPage").then((module) => ({ default: module.CustomerInsightsPage })));
const LogsPage = lazy(() => import("../pages/LogsPage").then((module) => ({ default: module.LogsPage })));
const RoutingPage = lazy(() => import("../pages/RoutingPage").then((module) => ({ default: module.RoutingPage })));
const PlaygroundPage = lazy(() => import("../pages/PlaygroundPage").then((module) => ({ default: module.PlaygroundPage })));
const VirtualKeysPage = lazy(() => import("../pages/VirtualKeysPage").then((module) => ({ default: module.VirtualKeysPage })));
const VirtualKeyDetailsPage = lazy(() => import("../pages/VirtualKeyDetailsPage").then((module) => ({ default: module.VirtualKeyDetailsPage })));
const ModelCatalogPage = lazy(() => import("../pages/ModelCatalogPage").then((module) => ({ default: module.ModelCatalogPage })));
const ModelOnboardingPage = lazy(() => import("../pages/ModelOnboardingPage").then((module) => ({ default: module.ModelOnboardingPage })));
const ProvidersPage = lazy(() => import("../pages/ProvidersPage").then((module) => ({ default: module.ProvidersPage })));
const CredentialsPage = lazy(() => import("../pages/CredentialsPage").then((module) => ({ default: module.CredentialsPage })));
const DeploymentsPage = lazy(() => import("../pages/DeploymentsPage").then((module) => ({ default: module.DeploymentsPage })));
const ModelGroupsPage = lazy(() => import("../pages/ModelGroupsPage").then((module) => ({ default: module.ModelGroupsPage })));
const OrganizationsPage = lazy(() => import("../pages/IdentityAssociationPages").then((module) => ({ default: module.OrganizationsPage })));
const TeamsPage = lazy(() => import("../pages/IdentityAssociationPages").then((module) => ({ default: module.TeamsPage })));
const OrganizationDetailsPage = lazy(() => import("../pages/OrganizationDetailsPage").then((module) => ({ default: module.OrganizationDetailsPage })));
const TeamDetailsPage = lazy(() => import("../pages/TeamDetailsPage").then((module) => ({ default: module.TeamDetailsPage })));
const AccessGroupsPage = lazy(() => import("../pages/AccessGroupsPage").then((module) => ({ default: module.AccessGroupsPage })));
const ProjectsPage = lazy(() => import("../pages/ProjectsPage").then((module) => ({ default: module.ProjectsPage })));
const ProjectDetailsPage = lazy(() => import("../pages/ProjectsPage").then((module) => ({ default: module.ProjectDetailsPage })));
const GuardrailsPage = lazy(() => import("../pages/GuardrailsPage").then((module) => ({ default: module.GuardrailsPage })));
const PoliciesPage = lazy(() => import("../pages/PoliciesPage").then((module) => ({ default: module.PoliciesPage })));
const BudgetDetailsPage = lazy(() => import("../pages/BudgetDetailsPage").then((module) => ({ default: module.BudgetDetailsPage })));
const BudgetsPage = lazy(() => import("../pages/BudgetsPage").then((module) => ({ default: module.BudgetsPage })));
const GuardrailMonitorPage = lazy(() => import("../pages/GuardrailMonitorPage").then((module) => ({ default: module.GuardrailMonitorPage })));
const MCPServersPage = lazy(() => import("../pages/MCPPages").then((module) => ({ default: module.MCPServersPage })));
const MCPToolsetsPage = lazy(() => import("../pages/MCPPages").then((module) => ({ default: module.MCPToolsetsPage })));
const SkillsPage = lazy(() => import("../pages/SkillsPage").then((module) => ({ default: module.SkillsPage })));
const CachePage = lazy(() => import("../pages/CachePage").then((module) => ({ default: module.CachePage })));
const LoggingPage = lazy(() => import("../pages/LoggingPage").then((module) => ({ default: module.LoggingPage })));
const RouterSettingsPage = lazy(() => import("../pages/RouterSettingsPage").then((module) => ({ default: module.RouterSettingsPage })));
const EndpointPage = lazy(() => import("../pages/EndpointPage").then((module) => ({ default: module.EndpointPage })));

const readOnly = (title: string, description: string, path: string, columns: ResourceConfig["columns"]): ReactNode => <ResourcePage config={{ eyebrow: "Operations", title, description, listPath: path, columns }} />;
const unavailable = (title: string, description: string): ReactNode => <CapabilityPage title={title} description={description} />;

function UsersPage() {
  const { hasCapability } = useAuth();
  return <ResourcePage config={resourceConfigs.users} readOnly={!hasCapability("admin")} />;
}

export function routeCapability(route: AppRoute): ConsoleCapability {
  return route.capability || "admin";
}

export const appRoutes: AppRoute[] = [
  { path: "/overview", title: "Overview", group: "Monitor", element: <OverviewPage />, available: true },
  { path: "/usage", title: "Usage & spend", group: "Monitor", element: <UsagePage />, available: true },
  { path: "/customers", title: "Customer insights", group: "Monitor", element: <CustomerInsightsPage />, available: true },
  { path: "/logs", title: "Logs", group: "Monitor", element: <LogsPage />, available: true },
  { path: "/routing", title: "Routing diagnostics", group: "Monitor", element: <RoutingPage />, available: true },
  { path: "/playground", title: "Playground", group: "Monitor", element: <PlaygroundPage />, available: true, capability: "inference" },

  { path: "/api-keys", title: "Virtual keys", group: "Manage", element: <VirtualKeysPage />, available: true },
  { path: "/api-keys/:id", title: "Virtual key details", group: "Manage", element: <VirtualKeyDetailsPage />, available: true, navigation: false },
  { path: "/models", title: "Models", group: "Manage", element: <ModelCatalogPage />, available: true },
  { path: "/model-onboarding", title: "Model onboarding", group: "Manage", element: <ModelOnboardingPage />, available: true },
  { path: "/providers", title: "Providers", group: "Manage", element: <ProvidersPage />, available: true },
  { path: "/credentials", title: "Credentials", group: "Manage", element: <CredentialsPage />, available: true },
  { path: "/deployments", title: "Deployments", group: "Manage", element: <DeploymentsPage />, available: true },
  { path: "/model-groups", title: "Model groups", group: "Manage", element: <ModelGroupsPage />, available: true },

  { path: "/organizations", title: "Organizations", group: "Access Control", element: <OrganizationsPage />, available: true },
  { path: "/organizations/:id", title: "Organization details", group: "Access Control", element: <OrganizationDetailsPage />, available: true, navigation: false },
  { path: "/teams", title: "Teams", group: "Access Control", element: <TeamsPage />, available: true, capability: "team_directory" },
  { path: "/teams/:id", title: "Team details", group: "Access Control", element: <TeamDetailsPage />, available: true, capability: "team_directory", navigation: false },
  { path: "/users", title: "Users", group: "Access Control", element: <UsersPage />, available: true, capability: "team_directory" },
  { path: "/access-groups", title: "Access groups", group: "Access Control", element: <AccessGroupsPage />, available: true },
  { path: "/access-groups/:id", title: "Access group details", group: "Access Control", element: <AccessGroupsPage />, available: true, navigation: false },
  { path: "/projects", title: "Projects", group: "Access Control", element: <ProjectsPage />, available: true },
  { path: "/projects/:id", title: "Project details", group: "Access Control", element: <ProjectDetailsPage />, available: true, navigation: false },

  { path: "/ai-hub", title: "AI Hub", group: "AI Hub", element: readOnly("AI Hub", "Catalog entries joined with safe runtime availability.", "/admin/v1/ai-hub/models", [{ key: "model", label: "Model" }, { key: "provider", label: "Provider" }, { key: "capabilities", label: "Capabilities" }, { key: "deployments", label: "Deployments" }, { key: "available", label: "Available" }]), available: true },
  { path: "/cost-optimization", title: "Cost optimization", group: "AI Hub", element: readOnly("Cost optimization", "Deterministic catalog and availability recommendations.", "/admin/v1/cost-optimization/recommendations", [{ key: "type", label: "Type" }, { key: "model", label: "Model" }, { key: "current_provider", label: "Current provider" }, { key: "recommended_provider", label: "Recommended provider" }, { key: "estimated_savings_percent", label: "Savings, %" }, { key: "summary", label: "Summary" }]), available: true },
  { path: "/mcp-servers", title: "MCP servers", group: "AI Hub", element: <MCPServersPage />, available: true },
  { path: "/mcp-toolsets", title: "MCP toolsets", group: "AI Hub", element: <MCPToolsetsPage />, available: true },
  { path: "/tool-policies", title: "Tool policies", group: "AI Hub", element: <ResourcePage config={resourceConfigs.toolPolicies} />, available: true },
  { path: "/agents", title: "Agent profiles", group: "AI Hub", element: <ResourcePage config={resourceConfigs.agents} />, available: true },
  { path: "/search-tools", title: "Search tools", group: "AI Hub", element: unavailable("Search tools", "A managed search-tool registry requires a dedicated execution adapter and credential boundary."), available: false },
  { path: "/skills", title: "Skills", group: "AI Hub", element: <SkillsPage />, available: true, capability: "inference" },

  { path: "/guardrails", title: "Guardrails", group: "Govern", element: <GuardrailsPage />, available: true },
  { path: "/guardrails-monitor", title: "Guardrail monitor", group: "Govern", element: <GuardrailMonitorPage />, available: true },
  { path: "/guardrails-monitor/:module", title: "Guardrail details", group: "Govern", element: <GuardrailMonitorPage />, available: true, navigation: false },
  { path: "/budgets", title: "Budgets", group: "Govern", element: <BudgetsPage />, available: true },
  { path: "/budgets/:id", title: "Budget details", group: "Govern", element: <BudgetDetailsPage />, available: true, navigation: false },
  { path: "/policies", title: "Policies", group: "Govern", element: <PoliciesPage />, available: true },
  { path: "/tag-management", title: "Tag management", group: "Govern", element: <ResourcePage config={resourceConfigs.tags} />, available: true },
  { path: "/cache", title: "Caching", group: "System", element: <CachePage />, available: true },
  { path: "/logging", title: "Logging & alerts", group: "System", element: <LoggingPage />, available: true },
  { path: "/router-settings", title: "Router settings", group: "System", element: <RouterSettingsPage />, available: true },
  { path: "/api-reference", title: "API reference", group: "System", element: <CapabilityPage title="API reference" description="The embedded OpenAPI and Swagger UI are available at /docs/." status="Open /docs/ in a new tab for the interactive contract." available />, available: true, capability: "api_docs" },
  { path: "/settings", title: "Settings", group: "System", element: <CapabilityPage title="Settings" description="Runtime settings remain environment-managed to preserve auditable deployment configuration." status="Environment-managed configuration" available />, available: true }
];
