import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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
      if (path === "/admin/v1/routing/simulate") return response({ strategy: "weighted", selected: "fallback", reason: "eligible", candidates: [] });
      return response({});
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<AuthProvider><RouterSettingsPage /></AuthProvider>);

    expect(await screen.findByDisplayValue("public-chat")).toBeInTheDocument();
    await userEvent.clear(screen.getByLabelText("Retries rate_limit"));
    await userEvent.type(screen.getByLabelText("Retries rate_limit"), "3");
    await userEvent.click(screen.getByLabelText("Move fallback up"));
    await userEvent.click(screen.getByRole("button", { name: "Save and simulate" }));

    await waitFor(() => expect(screen.getByText(/Router settings saved/)).toBeInTheDocument());
    const groupCall = fetchMock.mock.calls.find(([path, options]) => String(path).endsWith("/model-groups/public-chat") && options?.method === "PUT")!;
    expect(JSON.parse(String(groupCall[1]?.body))).toMatchObject({ deployment_ids: ["fallback", "primary"], retry_policy: { timeout: 1, rate_limit: 3 } });
    const fallbackCall = fetchMock.mock.calls.find(([path, options]) => String(path).endsWith("/model-deployments/fallback") && options?.method === "PUT")!;
    expect(JSON.parse(String(fallbackCall[1]?.body)).priority).toBe(0);
    expect(fetchMock.mock.calls.some(([path]) => String(path) === "/admin/v1/routing/simulate")).toBe(true);
    expect(await screen.findByText(/"selected": "fallback"/)).toBeInTheDocument();
  });
});
