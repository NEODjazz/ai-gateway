import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { RouterSettingsPage } from "./RouterSettingsPage";

function response(value: unknown) {
  return Promise.resolve(new Response(JSON.stringify(value), { status: 200, headers: { "Content-Type": "application/json" } }));
}

describe("RouterSettingsPage", () => {
  it("saves retry, fallback and deployment settings before simulating", async () => {
    const groups = [{ id: "public-chat", deployment_ids: ["primary", "fallback"], strategy: "weighted", retry_policy: { timeout: 1 }, enabled: true }];
    const deployments = [
      { id: "primary", provider_id: "azure", models: ["public-chat"], upstream_model: "gpt-a", priority: 0, weight: 2, max_retries: 1, request_timeout_ms: 1000, cooldown_after_failures: 2, cooldown_seconds: 20, enabled: true },
      { id: "fallback", provider_id: "ollama", models: ["public-chat"], upstream_model: "llama", priority: 1, weight: 1, max_retries: 0, request_timeout_ms: 2000, cooldown_after_failures: 3, cooldown_seconds: 30, enabled: true }
    ];
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const path = String(input);
      if (path === "/admin/v1/model-groups" && !options?.method) return response({ data: groups });
      if (path === "/admin/v1/model-deployments" && !options?.method) return response({ data: deployments });
      if (path === "/admin/v1/model-groups/public-chat/routing-settings" && !options?.method) return response({ revision: 7, model_group: groups[0], deployments });
      if (path === "/admin/v1/model-groups/public-chat/routing-settings" && options?.method === "PUT") {
        const body = JSON.parse(String(options.body));
        return response({ revision: 8, model_group: { ...groups[0], deployment_ids: body.deployment_ids, retry_policy: body.retry_policy }, deployments: body.deployments.map((row: { id: string }) => ({ ...deployments.find((item) => item.id === row.id), ...row })) });
      }
      if (path === "/admin/v1/routing/simulate") return response({ strategy: "weighted", selected: "fallback", reason: "eligible", candidates: [] });
      return response({});
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<MemoryRouter initialEntries={["/router-settings?group=public-chat"]}><AuthProvider><RouterSettingsPage /></AuthProvider></MemoryRouter>);

    expect(await screen.findByDisplayValue("public-chat")).toBeInTheDocument();
    expect(screen.getByText(/control-plane revision 7/)).toBeInTheDocument();
    await userEvent.clear(screen.getByLabelText("Retries rate_limit"));
    await userEvent.type(screen.getByLabelText("Retries rate_limit"), "3");
    await userEvent.click(screen.getByLabelText("Move fallback up"));
    await userEvent.click(screen.getByRole("button", { name: "Save and simulate" }));

    await waitFor(() => expect(screen.getByText(/Routing plan committed atomically/)).toBeInTheDocument());
    const routingCall = fetchMock.mock.calls.find(([path, options]) => String(path).endsWith("/model-groups/public-chat/routing-settings") && options?.method === "PUT")!;
    const routingBody = JSON.parse(String(routingCall[1]?.body));
    expect(routingBody).toMatchObject({ expected_revision: 7, deployment_ids: ["fallback", "primary"], retry_policy: { timeout: 1, rate_limit: 3 }, deployments: [{ id: "fallback", priority: 0 }, { id: "primary", priority: 1 }] });
    expect(routingBody.deployments.every((deployment: Record<string, unknown>) => !("enabled" in deployment))).toBe(true);
    expect(fetchMock.mock.calls.some(([path, options]) => String(path).includes("/model-deployments/") && options?.method === "PUT")).toBe(false);
    expect(screen.getByText(/Loaded from control-plane revision 8/)).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([path]) => String(path) === "/admin/v1/routing/simulate")).toBe(true);
    expect(await screen.findByText(/"selected": "fallback"/)).toBeInTheDocument();
  });
});
