package gateway

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type RouteContract struct {
	Method string
	Path   string
}

type routeDefinition struct {
	RouteContract
	handler func(Handler) http.Handler
}

var gatewayRoutes = []routeDefinition{
	{RouteContract{http.MethodGet, "/healthz"}, func(h Handler) http.Handler { return http.HandlerFunc(h.Health) }},
	{RouteContract{http.MethodGet, "/readyz"}, func(h Handler) http.Handler { return http.HandlerFunc(h.Ready) }},
	{RouteContract{http.MethodGet, "/metrics"}, func(h Handler) http.Handler { return h.metrics }},
	{RouteContract{http.MethodGet, "/v1/models"}, func(h Handler) http.Handler { return http.HandlerFunc(h.Models) }},
	{RouteContract{http.MethodPost, "/v1/chat/completions"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ChatCompletions) }},
	{RouteContract{http.MethodPost, "/v1/messages"}, func(h Handler) http.Handler { return http.HandlerFunc(h.Messages) }},
	{RouteContract{http.MethodPost, "/v1/messages/count_tokens"}, func(h Handler) http.Handler { return http.HandlerFunc(h.CountMessageTokens) }},
	{RouteContract{http.MethodPost, "/v1/responses"}, func(h Handler) http.Handler { return http.HandlerFunc(h.Responses) }},
	{RouteContract{http.MethodGet, "/v1/responses/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.GetResponse) }},
	{RouteContract{http.MethodPost, "/v1/responses/{id}/cancel"}, func(h Handler) http.Handler { return http.HandlerFunc(h.CancelResponse) }},
	{RouteContract{http.MethodPost, "/v1/embeddings"}, func(h Handler) http.Handler { return http.HandlerFunc(h.Embeddings) }},
	{RouteContract{http.MethodPost, "/v1/rerank"}, func(h Handler) http.Handler { return http.HandlerFunc(h.Rerank) }},
	{RouteContract{http.MethodGet, "/admin/v1/session"}, func(h Handler) http.Handler { return http.HandlerFunc(h.GetAdminSession) }},
	{RouteContract{http.MethodGet, "/admin/v1/keys"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListVirtualKeys) }},
	{RouteContract{http.MethodPost, "/admin/v1/keys"}, func(h Handler) http.Handler { return http.HandlerFunc(h.CreateVirtualKey) }},
	{RouteContract{http.MethodPost, "/admin/v1/keys/{id}/rotate"}, func(h Handler) http.Handler { return http.HandlerFunc(h.RotateVirtualKey) }},
	{RouteContract{http.MethodPut, "/admin/v1/keys/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.UpdateVirtualKey) }},
	{RouteContract{http.MethodPost, "/admin/v1/keys/{id}/disable"}, func(h Handler) http.Handler { return http.HandlerFunc(h.DisableVirtualKey) }},
	{RouteContract{http.MethodPost, "/admin/v1/keys/{id}/enable"}, func(h Handler) http.Handler { return http.HandlerFunc(h.EnableVirtualKey) }},
	{RouteContract{http.MethodDelete, "/admin/v1/keys/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.RevokeVirtualKey) }},
	{RouteContract{http.MethodGet, "/admin/v1/users"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListDirectoryUsers) }},
	{RouteContract{http.MethodPut, "/admin/v1/users/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.PutDirectoryUser) }},
	{RouteContract{http.MethodGet, "/admin/v1/teams"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListDirectoryTeams) }},
	{RouteContract{http.MethodPut, "/admin/v1/teams/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.PutDirectoryTeam) }},
	{RouteContract{http.MethodPut, "/admin/v1/teams/{id}/members/{user_id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.PutTeamMembership) }},
	{RouteContract{http.MethodGet, "/admin/v1/teams/{id}/members"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListTeamMemberships) }},
	{RouteContract{http.MethodDelete, "/admin/v1/teams/{id}/members/{user_id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.DeleteTeamMembership) }},
	{RouteContract{http.MethodGet, "/admin/v1/organizations"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListOrganizations) }},
	{RouteContract{http.MethodPut, "/admin/v1/organizations/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.PutOrganization) }},
	{RouteContract{http.MethodPut, "/admin/v1/organizations/{id}/teams/{team_id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.PutOrganizationTeam) }},
	{RouteContract{http.MethodDelete, "/admin/v1/organizations/{id}/teams/{team_id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.DeleteOrganizationTeam) }},
	{RouteContract{http.MethodGet, "/admin/v1/budgets"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListBudgets) }},
	{RouteContract{http.MethodPost, "/admin/v1/budgets"}, func(h Handler) http.Handler { return http.HandlerFunc(h.CreateBudget) }},
	{RouteContract{http.MethodGet, "/admin/v1/budgets/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.GetBudget) }},
	{RouteContract{http.MethodPut, "/admin/v1/budgets/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.UpdateBudget) }},
	{RouteContract{http.MethodDelete, "/admin/v1/budgets/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.DisableBudget) }},
	{RouteContract{http.MethodGet, "/admin/v1/budgets/{id}/summary"}, func(h Handler) http.Handler { return http.HandlerFunc(h.GetBudgetSummary) }},
	{RouteContract{http.MethodGet, "/admin/v1/usage/report"}, func(h Handler) http.Handler { return http.HandlerFunc(h.GetUsageReport) }},
	{RouteContract{http.MethodGet, "/admin/v1/customers/{scope_type}/{scope_id}/usage"}, func(h Handler) http.Handler { return http.HandlerFunc(h.GetCustomerUsageReport) }},
	{RouteContract{http.MethodGet, "/admin/v1/request-logs"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListRequestLogs) }},
	{RouteContract{http.MethodGet, "/admin/v1/request-logs/groups"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListRequestLogGroups) }},
	{RouteContract{http.MethodGet, "/admin/v1/request-logs/settings"}, func(h Handler) http.Handler { return http.HandlerFunc(h.GetRequestLogSettings) }},
	{RouteContract{http.MethodGet, "/admin/v1/request-logs/{request_id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.GetRequestLog) }},
	{RouteContract{http.MethodGet, "/admin/v1/routing/diagnostics"}, func(h Handler) http.Handler { return http.HandlerFunc(h.GetRoutingDiagnostics) }},
	{RouteContract{http.MethodPost, "/admin/v1/routing/simulate"}, func(h Handler) http.Handler { return http.HandlerFunc(h.SimulateRouting) }},
	{RouteContract{http.MethodGet, "/admin/v1/model-catalog"}, func(h Handler) http.Handler { return http.HandlerFunc(h.GetModelCatalog) }},
	{RouteContract{http.MethodPost, "/admin/v1/model-onboarding/plan"}, func(h Handler) http.Handler { return http.HandlerFunc(h.PlanModelOnboarding) }},
	{RouteContract{http.MethodPost, "/admin/v1/model-onboarding/apply"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ApplyModelOnboarding) }},
	{RouteContract{http.MethodGet, "/admin/v1/providers"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListProviders) }},
	{RouteContract{http.MethodPost, "/admin/v1/providers"}, func(h Handler) http.Handler { return http.HandlerFunc(h.CreateProvider) }},
	{RouteContract{http.MethodPut, "/admin/v1/providers/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.UpdateProvider) }},
	{RouteContract{http.MethodDelete, "/admin/v1/providers/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.DeleteProvider) }},
	{RouteContract{http.MethodPost, "/admin/v1/providers/{id}/test"}, func(h Handler) http.Handler { return http.HandlerFunc(h.TestProviderConnection) }},
	{RouteContract{http.MethodPost, "/admin/v1/providers/{id}/discover-models"}, func(h Handler) http.Handler { return http.HandlerFunc(h.DiscoverProviderModels) }},
	{RouteContract{http.MethodGet, "/admin/v1/credentials"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListCredentials) }},
	{RouteContract{http.MethodPost, "/admin/v1/credentials"}, func(h Handler) http.Handler { return http.HandlerFunc(h.CreateCredential) }},
	{RouteContract{http.MethodPut, "/admin/v1/credentials/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.UpdateCredential) }},
	{RouteContract{http.MethodPost, "/admin/v1/credentials/{id}/rotate"}, func(h Handler) http.Handler { return http.HandlerFunc(h.RotateCredential) }},
	{RouteContract{http.MethodDelete, "/admin/v1/credentials/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.DeleteCredential) }},
	{RouteContract{http.MethodPut, "/admin/v1/model-catalog"}, func(h Handler) http.Handler { return http.HandlerFunc(h.PutModelCatalog) }},
	{RouteContract{http.MethodGet, "/admin/v1/ai-hub/models"}, func(h Handler) http.Handler { return http.HandlerFunc(h.GetAIHubModels) }},
	{RouteContract{http.MethodGet, "/admin/v1/cost-optimization/recommendations"}, func(h Handler) http.Handler { return http.HandlerFunc(h.GetCostRecommendations) }},
	{RouteContract{http.MethodGet, "/admin/v1/model-deployments"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListModelDeployments) }},
	{RouteContract{http.MethodGet, "/admin/v1/model-deployments/health"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListLatestModelDeploymentHealth) }},
	{RouteContract{http.MethodPost, "/admin/v1/model-deployments/health-checks"}, func(h Handler) http.Handler { return http.HandlerFunc(h.TestModelDeployments) }},
	{RouteContract{http.MethodPost, "/admin/v1/model-deployments"}, func(h Handler) http.Handler { return http.HandlerFunc(h.CreateModelDeployment) }},
	{RouteContract{http.MethodPut, "/admin/v1/model-deployments/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.UpdateModelDeployment) }},
	{RouteContract{http.MethodDelete, "/admin/v1/model-deployments/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.DeleteModelDeployment) }},
	{RouteContract{http.MethodPost, "/admin/v1/model-deployments/{id}/test"}, func(h Handler) http.Handler { return http.HandlerFunc(h.TestModelDeployment) }},
	{RouteContract{http.MethodGet, "/admin/v1/model-deployments/{id}/health"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListModelDeploymentHealth) }},
	{RouteContract{http.MethodGet, "/admin/v1/model-groups"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListModelGroups) }},
	{RouteContract{http.MethodPost, "/admin/v1/model-groups"}, func(h Handler) http.Handler { return http.HandlerFunc(h.CreateModelGroup) }},
	{RouteContract{http.MethodPut, "/admin/v1/model-groups/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.UpdateModelGroup) }},
	{RouteContract{http.MethodDelete, "/admin/v1/model-groups/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.DeleteModelGroup) }},
	{RouteContract{http.MethodGet, "/admin/v1/model-groups/{id}/routing-settings"}, func(h Handler) http.Handler { return http.HandlerFunc(h.GetModelGroupRouting) }},
	{RouteContract{http.MethodPut, "/admin/v1/model-groups/{id}/routing-settings"}, func(h Handler) http.Handler { return http.HandlerFunc(h.UpdateModelGroupRouting) }},
	{RouteContract{http.MethodGet, "/admin/v1/guardrail-policies"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListGuardrailPolicies) }},
	{RouteContract{http.MethodPut, "/admin/v1/guardrail-policies/{name}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.UpdateGuardrailPolicy) }},
	{RouteContract{http.MethodGet, "/admin/v1/policy-attachments"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListPolicyAttachments) }},
	{RouteContract{http.MethodPost, "/admin/v1/policy-attachments/resolve"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ResolvePolicyAttachments) }},
	{RouteContract{http.MethodPut, "/admin/v1/policy-attachments/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.PutPolicyAttachment) }},
	{RouteContract{http.MethodDelete, "/admin/v1/policy-attachments/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.DeletePolicyAttachment) }},
	{RouteContract{http.MethodGet, "/admin/v1/tags"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListTags) }},
	{RouteContract{http.MethodPut, "/admin/v1/tags/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.PutTag) }},
	{RouteContract{http.MethodDelete, "/admin/v1/tags/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.DeleteTag) }},
	{RouteContract{http.MethodPost, "/admin/v1/compliance/check"}, func(h Handler) http.Handler { return http.HandlerFunc(h.CheckCompliance) }},
	{RouteContract{http.MethodGet, "/admin/v1/guardrails/monitor"}, func(h Handler) http.Handler { return http.HandlerFunc(h.GetGuardrailMonitor) }},
	{RouteContract{http.MethodGet, "/admin/v1/cache/diagnostics"}, func(h Handler) http.Handler { return http.HandlerFunc(h.GetCacheDiagnostics) }},
	{RouteContract{http.MethodGet, "/admin/v1/logging/destinations"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListLoggingDestinations) }},
	{RouteContract{http.MethodPut, "/admin/v1/logging/destinations/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.PutLoggingDestination) }},
	{RouteContract{http.MethodDelete, "/admin/v1/logging/destinations/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.DeleteLoggingDestination) }},
	{RouteContract{http.MethodPost, "/admin/v1/logging/destinations/{id}/test"}, func(h Handler) http.Handler { return http.HandlerFunc(h.TestLoggingDestination) }},
	{RouteContract{http.MethodGet, "/admin/v1/tool-policies"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListToolPolicies) }},
	{RouteContract{http.MethodPut, "/admin/v1/tool-policies/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.PutToolPolicy) }},
	{RouteContract{http.MethodDelete, "/admin/v1/tool-policies/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.DeleteToolPolicy) }},
	{RouteContract{http.MethodGet, "/admin/v1/agent-profiles"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListAgentProfiles) }},
	{RouteContract{http.MethodPut, "/admin/v1/agent-profiles/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.PutAgentProfile) }},
	{RouteContract{http.MethodDelete, "/admin/v1/agent-profiles/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.DeleteAgentProfile) }},
	{RouteContract{http.MethodGet, "/admin/v1/mcp/servers"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListMCPServers) }},
	{RouteContract{http.MethodPut, "/admin/v1/mcp/servers/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.UpdateMCPServer) }},
	{RouteContract{http.MethodDelete, "/admin/v1/mcp/servers/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.DeleteMCPServer) }},
	{RouteContract{http.MethodGet, "/admin/v1/mcp/toolsets"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListMCPToolsets) }},
	{RouteContract{http.MethodPut, "/admin/v1/mcp/toolsets/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.UpdateMCPToolset) }},
	{RouteContract{http.MethodDelete, "/admin/v1/mcp/toolsets/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.DeleteMCPToolset) }},
	{RouteContract{http.MethodGet, "/admin/v1/projects"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListProjects) }},
	{RouteContract{http.MethodGet, "/admin/v1/projects/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.GetProject) }},
	{RouteContract{http.MethodPut, "/admin/v1/projects/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.PutProject) }},
	{RouteContract{http.MethodDelete, "/admin/v1/projects/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.DeleteProject) }},
	{RouteContract{http.MethodGet, "/admin/v1/access-groups"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListAccessGroups) }},
	{RouteContract{http.MethodGet, "/admin/v1/access-groups/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.GetAccessGroup) }},
	{RouteContract{http.MethodPut, "/admin/v1/access-groups/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.PutAccessGroup) }},
	{RouteContract{http.MethodDelete, "/admin/v1/access-groups/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.DeleteAccessGroup) }},
	{RouteContract{http.MethodGet, "/admin/v1/audit/events"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListAuditEvents) }},
}

func DocumentedRoutes() []RouteContract {
	routes := make([]RouteContract, len(gatewayRoutes))
	for index, route := range gatewayRoutes {
		routes[index] = route.RouteContract
	}
	return append(routes, generateRoutes...)
}

func Routes(handler Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1beta/models/{modelAction}", handler.GenerateContent)
	for _, route := range gatewayRoutes {
		h := route.handler(handler)
		if handler.adminState != nil && isDurableAdminStateRoute(route.Path) {
			h = handler.adminState.Wrap(h, isDurableAdminStateMutation(route.Method, route.Path))
		}
		mux.Handle(route.Method+" "+route.Path, h)
	}
	if handler.apiDocs.enabled {
		registerAPIDocs(mux, handler.apiDocs)
	}
	if handler.adminUI {
		registerAdminUI(mux)
	}
	observed := observabilityMiddleware(handler.metrics, mux)
	return otelhttp.NewHandler(observed, "ai-gateway.http",
		otelhttp.WithFilter(func(r *http.Request) bool {
			return !isInfrastructurePath(r.URL.Path)
		}),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return metricMethod(r.Method) + " " + metricPath(r.URL.Path)
		}),
	)
}
