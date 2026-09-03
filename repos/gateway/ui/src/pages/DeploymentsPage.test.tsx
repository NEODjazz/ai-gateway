import { render, screen, waitFor, within } from "@testing-library/react";
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
      if (path.includes("/health")) return response({ data: [check], errors: [] });
      if (path === "/admin/v1/model-deployments/azure-gpt" && options?.method === "PUT") return response({ ...deployment, enabled: false });
      return response({ data: [] });
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<MemoryRouter><AuthProvider><DeploymentsPage /></AuthProvider></MemoryRouter>);

    expect(await screen.findByText("azure-gpt")).toBeInTheDocument();
    const selectPage = within(screen.getAllByRole("columnheader")[0]).getByRole("checkbox");
    expect(screen.queryByText("Select page")).not.toBeInTheDocument();
    await userEvent.click(selectPage);
    expect(selectPage).toBeChecked();
    expect(screen.getByRole("button", { name: "Check selected (1)" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Columns" }));
    expect(screen.queryByRole("menuitemcheckbox", { name: "Selected" })).not.toBeInTheDocument();
    expect(screen.getByRole("menuitemcheckbox", { name: "Deployment" })).toBeDisabled();
    expect(fetchMock.mock.calls.some(([path]) => String(path).includes("sort=priority") && String(path).includes("limit=25"))).toBe(true);
    await userEvent.type(screen.getByLabelText("Search deployments"), "azure");
    await userEvent.click(screen.getByRole("button", { name: "Filter" }));
    const filters = await screen.findByRole("dialog", { name: "Filter deployments" });
    await userEvent.type(within(filters).getByLabelText("Provider"), "azure");
    await userEvent.selectOptions(within(filters).getByLabelText("Runtime"), "available");
    await userEvent.click(within(filters).getByRole("button", { name: "Apply filters" }));
    await userEvent.click(screen.getByRole("button", { name: /Provider/ }));
    await userEvent.click(screen.getByRole("button", { name: /Provider/ }));
    expect(screen.getByText("42 ms")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Actions for azure-gpt" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Details" }));
    expect(await screen.findByRole("dialog", { name: "Deployment details" })).toBeInTheDocument();
    expect(screen.getByText("2026-08-27 18:00:00 UTC")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Run health check" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path, options]) => String(path) === "/admin/v1/model-deployments/health-checks" && options?.method === "POST")).toBe(true));
    const healthCall = fetchMock.mock.calls.find(([path, options]) => String(path) === "/admin/v1/model-deployments/health-checks" && options?.method === "POST")!;
    expect(JSON.parse(String(healthCall[1]?.body))).toEqual({ deployment_ids: ["azure-gpt"] });
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    await userEvent.click(screen.getByRole("button", { name: "Actions for azure-gpt" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Pause" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path, options]) => String(path) === "/admin/v1/model-deployments/azure-gpt" && options?.method === "PUT")).toBe(true));
    const pauseCall = fetchMock.mock.calls.find(([path, options]) => String(path) === "/admin/v1/model-deployments/azure-gpt" && options?.method === "PUT")!;
    expect(JSON.parse(String(pauseCall[1]?.body)).enabled).toBe(false);
  });
});
