import type { ReactNode } from "react";
import { CapabilityPage } from "../components/CapabilityPage";
import { ResourcePage, type ResourceConfig } from "../components/ResourcePage";
import { EndpointPage } from "../pages/EndpointPage";
import { CustomerInsightsPage } from "../pages/CustomerInsightsPage";
import { OverviewPage } from "../pages/OverviewPage";
import { ModelCatalogPage } from "../pages/ModelCatalogPage";
import { PlaygroundPage } from "../pages/PlaygroundPage";
import { RequestLogsPage } from "../pages/RequestLogsPage";
import { RoutingPage } from "../pages/RoutingPage";
import { GuardrailsPage } from "../pages/GuardrailsPage";
import { OrganizationsPage, TeamsPage } from "../pages/IdentityAssociationPages";
import { UsagePage } from "../pages/UsagePage";
import { ModelOnboardingPage } from "../pages/ModelOnboardingPage";
import { DeploymentsPage } from "../pages/DeploymentsPage";
import { RouterSettingsPage } from "../pages/RouterSettingsPage";
import { resourceConfigs } from "../pages/resourceConfigs";

export type AppRoute = { path: string; title: string; group: "Monitor" | "Manage" | "AI Hub" | "Govern" | "System"; element: ReactNode; available: boolean };

const readOnly = (title: string, description: string, path: string, columns: ResourceConfig["columns"]): ReactNode => <ResourcePage config={{ eyebrow: "Operations", title, description, listPath: path, columns }} />;
const unavailable = (title: string, description: string): ReactNode => <CapabilityPage title={title} description={description} />;

export const appRoutes: AppRoute[] = [
  { path: "/overview", title: "Overview", group: "Monitor", element: <OverviewPage />, available: true },
  { path: "/usage", title: "Usage & spend", group: "Monitor", element: <UsagePage />, available: true },
  { path: "/customers", title: "Customer insights", group: "Monitor", element: <CustomerInsightsPage />, available: true },
  { path: "/request-logs", title: "Request logs", group: "Monitor", element: <RequestLogsPage />, available: true },
  { path: "/routing", title: "Routing diagnostics", group: "Monitor", element: <RoutingPage />, available: true },
  { path: "/playground", title: "Playground", group: "Monitor", element: <PlaygroundPage />, available: true },

  { path: "/api-keys", title: "Virtual keys", group: "Manage", element: <ResourcePage config={resourceConfigs.keys} />, available: true },
  { path: "/users", title: "Users", group: "Manage", element: <ResourcePage config={resourceConfigs.users} />, available: true },
  { path: "/teams", title: "Teams", group: "Manage", element: <TeamsPage />, available: true },
  { path: "/organizations", title: "Organizations", group: "Manage", element: <OrganizationsPage />, available: true },
  { path: "/projects", title: "Projects", group: "Manage", element: <ResourcePage config={resourceConfigs.projects} />, available: true },
  { path: "/access-groups", title: "Access groups", group: "Manage", element: <ResourcePage config={resourceConfigs.accessGroups} />, available: true },
  { path: "/models", title: "Models", group: "Manage", element: <ModelCatalogPage />, available: true },
  { path: "/model-onboarding", title: "Model onboarding", group: "Manage", element: <ModelOnboardingPage />, available: true },
  { path: "/providers", title: "Providers", group: "Manage", element: <ResourcePage config={resourceConfigs.providers} />, available: true },
  { path: "/credentials", title: "Credentials", group: "Manage", element: <ResourcePage config={resourceConfigs.credentials} />, available: true },
  { path: "/deployments", title: "Deployments", group: "Manage", element: <DeploymentsPage />, available: true },
  { path: "/model-groups", title: "Model groups", group: "Manage", element: <ResourcePage config={resourceConfigs.modelGroups} />, available: true },

  { path: "/ai-hub", title: "AI Hub", group: "AI Hub", element: readOnly("AI Hub", "Catalog entries joined with safe runtime availability.", "/admin/v1/ai-hub/models", [{ key: "model", label: "Model" }, { key: "provider", label: "Provider" }, { key: "capabilities", label: "Capabilities" }, { key: "deployments", label: "Deployments" }, { key: "available", label: "Available" }]), available: true },
  { path: "/cost-optimization", title: "Cost optimization", group: "AI Hub", element: readOnly("Cost optimization", "Deterministic catalog and availability recommendations.", "/admin/v1/cost-optimization/recommendations", [{ key: "type", label: "Type" }, { key: "model", label: "Model" }, { key: "current_provider", label: "Current provider" }, { key: "recommended_provider", label: "Recommended provider" }, { key: "estimated_savings_percent", label: "Savings, %" }, { key: "summary", label: "Summary" }]), available: true },
  { path: "/mcp-servers", title: "MCP servers", group: "AI Hub", element: <ResourcePage config={resourceConfigs.mcpServers} />, available: true },
  { path: "/mcp-toolsets", title: "MCP toolsets", group: "AI Hub", element: <ResourcePage config={resourceConfigs.mcpToolsets} />, available: true },
  { path: "/tool-policies", title: "Tool policies", group: "AI Hub", element: <ResourcePage config={resourceConfigs.toolPolicies} />, available: true },
  { path: "/agents", title: "Agent profiles", group: "AI Hub", element: <ResourcePage config={resourceConfigs.agents} />, available: true },
  { path: "/search-tools", title: "Search tools", group: "AI Hub", element: unavailable("Search tools", "A managed search-tool registry requires a dedicated execution adapter and credential boundary."), available: false },
  { path: "/skills", title: "Skills", group: "AI Hub", element: unavailable("Skills", "The gateway governs tool identities but does not store or execute skill content."), available: false },

  { path: "/guardrails", title: "Guardrails", group: "Govern", element: <GuardrailsPage />, available: true },
  { path: "/guardrails-monitor", title: "Guardrail monitor", group: "Govern", element: <EndpointPage eyebrow="Compliance" title="Guardrail monitor" description="Metadata-only check outcomes held in bounded runtime memory." path="/admin/v1/guardrails/monitor" />, available: true },
  { path: "/budgets", title: "Budgets", group: "Govern", element: <ResourcePage config={resourceConfigs.budgets} />, available: true },
  { path: "/policies", title: "Policies", group: "Govern", element: unavailable("Policies", "Cross-resource policy attachments are not yet part of the gateway API."), available: false },
  { path: "/tag-management", title: "Tag management", group: "Govern", element: unavailable("Tag management", "Tags are accepted on resources; a central validation and cost-allocation registry is planned."), available: false },
  { path: "/audit", title: "Audit log", group: "Govern", element: <ResourcePage config={resourceConfigs.audit} />, available: true },

  { path: "/cache", title: "Caching", group: "System", element: <EndpointPage eyebrow="Performance" title="Cache diagnostics" description="Exact and semantic cache counters and bounded configuration metadata." path="/admin/v1/cache/diagnostics" />, available: true },
  { path: "/logging", title: "Logging & alerts", group: "System", element: <ResourcePage config={resourceConfigs.logging} />, available: true },
  { path: "/router-settings", title: "Router settings", group: "System", element: <RouterSettingsPage />, available: true },
  { path: "/api-reference", title: "API reference", group: "System", element: <CapabilityPage title="API reference" description="The embedded OpenAPI and Swagger UI are available at /docs/." status="Open /docs/ in a new tab for the interactive contract." available />, available: true },
  { path: "/settings", title: "Settings", group: "System", element: <CapabilityPage title="Settings" description="Runtime settings remain environment-managed to preserve auditable deployment configuration." status="Environment-managed configuration" available />, available: true }
];
