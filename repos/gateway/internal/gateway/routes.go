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
	{RouteContract{http.MethodPost, "/v1/responses"}, func(h Handler) http.Handler { return http.HandlerFunc(h.Responses) }},
	{RouteContract{http.MethodPost, "/v1/embeddings"}, func(h Handler) http.Handler { return http.HandlerFunc(h.Embeddings) }},
	{RouteContract{http.MethodPost, "/admin/v1/keys"}, func(h Handler) http.Handler { return http.HandlerFunc(h.CreateVirtualKey) }},
	{RouteContract{http.MethodPost, "/admin/v1/keys/{id}/rotate"}, func(h Handler) http.Handler { return http.HandlerFunc(h.RotateVirtualKey) }},
	{RouteContract{http.MethodDelete, "/admin/v1/keys/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.RevokeVirtualKey) }},
	{RouteContract{http.MethodGet, "/admin/v1/budgets"}, func(h Handler) http.Handler { return http.HandlerFunc(h.ListBudgets) }},
	{RouteContract{http.MethodPost, "/admin/v1/budgets"}, func(h Handler) http.Handler { return http.HandlerFunc(h.CreateBudget) }},
	{RouteContract{http.MethodGet, "/admin/v1/budgets/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.GetBudget) }},
	{RouteContract{http.MethodPut, "/admin/v1/budgets/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.UpdateBudget) }},
	{RouteContract{http.MethodDelete, "/admin/v1/budgets/{id}"}, func(h Handler) http.Handler { return http.HandlerFunc(h.DisableBudget) }},
	{RouteContract{http.MethodGet, "/admin/v1/budgets/{id}/summary"}, func(h Handler) http.Handler { return http.HandlerFunc(h.GetBudgetSummary) }},
}

func DocumentedRoutes() []RouteContract {
	routes := make([]RouteContract, len(gatewayRoutes))
	for index, route := range gatewayRoutes {
		routes[index] = route.RouteContract
	}
	return routes
}

func Routes(handler Handler) http.Handler {
	mux := http.NewServeMux()
	for _, route := range gatewayRoutes {
		mux.Handle(route.Method+" "+route.Path, route.handler(handler))
	}
	if handler.apiDocs.enabled {
		registerAPIDocs(mux, handler.apiDocs)
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
