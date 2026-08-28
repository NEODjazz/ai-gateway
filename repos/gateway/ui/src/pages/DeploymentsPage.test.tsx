import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { DeploymentsPage } from "./DeploymentsPage";

function response(value: unknown) {
  return Promise.resolve(new Response(JSON.stringify(value), { status: 200, headers: { "Content-Type": "application/json" } }));
}

describe("DeploymentsPage", () => {
  it("shows health details, runs checks and pauses a deployment", async () => {
    const deployment = { id: "azure-gpt", provider_id: "azure", credential_id: "azure-key", provider_type: "openai-compatible", upstream_model: "gpt-versioned", models: ["gpt"], capabilities: ["chat"], priority: 0, weight: 1, enabled: true, runtime_state: "available", latency_ewma_ms: 100, failure_ewma: 0.01 };
    const check = { deployment_id: "azure-gpt", provider_id: "azure", model: "gpt-versioned", status: "available", latency_ms: 42, checked_at: "2026-08-27T18:00:00Z" };
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const path = String(input);
      if (path.startsWith("/admin/v1/model-deployments?") && !options?.method) return response({ data: [deployment], total: 1 });
      if (path.includes("/health")) return response({ data: [check] });
      if (path.endsWith("/test")) return response(check);
      if (path === "/admin/v1/model-deployments/azure-gpt" && options?.method === "PUT") return response({ ...deployment, enabled: false });
      return response({ data: [] });
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<MemoryRouter><AuthProvider><DeploymentsPage /></AuthProvider></MemoryRouter>);

    expect(await screen.findByText("azure-gpt")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([path]) => String(path).includes("sort=priority") && String(path).includes("limit=25"))).toBe(true);
    await userEvent.type(screen.getByLabelText("Search deployments"), "azure");
    await userEvent.type(screen.getByLabelText("Provider"), "azure");
    await userEvent.selectOptions(screen.getByLabelText("Runtime"), "available");
    await userEvent.selectOptions(screen.getByLabelText("Sort"), "provider");
    await userEvent.selectOptions(screen.getByLabelText("Order"), "desc");
    await userEvent.click(screen.getByRole("button", { name: "Apply" }));
    await userEvent.click(screen.getByLabelText("Select all deployments"));
    expect(screen.getByText("42 ms")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Actions for azure-gpt" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Details" }));
    expect(await screen.findByRole("dialog", { name: "Deployment details" })).toBeInTheDocument();
    expect(screen.getByText("2026-08-27 18:00:00 UTC")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Run health check" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path]) => String(path).endsWith("/test"))).toBe(true));
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    await userEvent.click(screen.getByRole("button", { name: "Actions for azure-gpt" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Pause" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path, options]) => String(path) === "/admin/v1/model-deployments/azure-gpt" && options?.method === "PUT")).toBe(true));
    const pauseCall = fetchMock.mock.calls.find(([path, options]) => String(path) === "/admin/v1/model-deployments/azure-gpt" && options?.method === "PUT")!;
    expect(JSON.parse(String(pauseCall[1]?.body)).enabled).toBe(false);
  });
});
