import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { ModelGroupsPage } from "./ModelGroupsPage";

function response(value: unknown) {
  return Promise.resolve(new Response(JSON.stringify(value), { status: 200, headers: { "Content-Type": "application/json" } }));
}

const deployments = [
  { id: "primary", provider_id: "azure", upstream_model: "gpt-versioned", priority: 0, weight: 3, enabled: true, runtime_state: "available" },
  { id: "fallback", provider_id: "ollama", upstream_model: "llama3.2", priority: 1, weight: 1, enabled: true, runtime_state: "available" }
];
const checks = [
  { deployment_id: "primary", status: "available", latency_ms: 42, checked_at: "2026-09-02T10:00:00Z" },
  { deployment_id: "fallback", status: "unavailable", latency_ms: 500, checked_at: "2026-09-02T10:00:00Z", failure_class: "timeout" }
];

function LocationProbe() {
  const location = useLocation();
  return <span data-testid="location">{location.pathname}{location.search}</span>;
}

describe("ModelGroupsPage", () => {
  it("shows route topology, runs bounded health checks and saves membership order and retry policy", async () => {
    const group = { id: "public-chat", deployment_ids: ["primary", "fallback"], strategy: "weighted", retry_policy: { timeout: 1 }, enabled: true };
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const path = String(input);
      if (path === "/admin/v1/model-groups" && !options?.method) return response({ data: [group] });
      if (path === "/admin/v1/model-deployments" && !options?.method) return response({ data: deployments });
      if (path.startsWith("/admin/v1/model-deployments/health?") && !options?.method) return response({ data: checks });
      if (path === "/admin/v1/model-deployments/health-checks" && options?.method === "POST") return response({ data: checks, errors: [] });
      if (path === "/admin/v1/model-groups/public-chat" && options?.method === "PUT") return response(group);
      return response({});
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<MemoryRouter><AuthProvider><ModelGroupsPage /><LocationProbe /></AuthProvider></MemoryRouter>);

    expect(await screen.findByText("1/2")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([path]) => String(path).includes("/health?ids=primary%2Cfallback"))).toBe(true);
    await userEvent.click(screen.getByRole("button", { name: "Actions for public-chat" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Routing details" }));
    const detail = await screen.findByRole("dialog", { name: "Model group routing details" });
    expect(within(detail).getByText("gpt-versioned")).toBeInTheDocument();
    expect(within(detail).getByText("42 ms")).toBeInTheDocument();
    await userEvent.click(within(detail).getByRole("button", { name: "Run health checks" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path, options]) => String(path) === "/admin/v1/model-deployments/health-checks" && options?.method === "POST")).toBe(true));
    const healthCall = fetchMock.mock.calls.find(([path, options]) => String(path) === "/admin/v1/model-deployments/health-checks" && options?.method === "POST")!;
    expect(JSON.parse(String(healthCall[1]?.body))).toEqual({ deployment_ids: ["primary", "fallback"] });
    await userEvent.click(within(detail).getByRole("button", { name: "Close routing details" }));

    await userEvent.click(screen.getByRole("button", { name: "Actions for public-chat" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Edit" }));
    const form = await screen.findByRole("dialog", { name: "Edit model group" });
    await userEvent.click(within(form).getByLabelText("Move fallback up"));
    await userEvent.clear(within(form).getByLabelText("Retries rate_limit"));
    await userEvent.type(within(form).getByLabelText("Retries rate_limit"), "2");
    await userEvent.click(within(form).getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path, options]) => String(path) === "/admin/v1/model-groups/public-chat" && options?.method === "PUT")).toBe(true));
    const update = fetchMock.mock.calls.find(([path, options]) => String(path) === "/admin/v1/model-groups/public-chat" && options?.method === "PUT")!;
    expect(JSON.parse(String(update[1]?.body))).toMatchObject({ deployment_ids: ["fallback", "primary"], retry_policy: { timeout: 1, rate_limit: 2 } });
    await userEvent.click(screen.getByRole("button", { name: "Actions for public-chat" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Configure routing" }));
    expect(screen.getByTestId("location")).toHaveTextContent("/router-settings?group=public-chat");
  });

  it("creates a group with searchable multi-select deployments", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const path = String(input);
      if (path === "/admin/v1/model-groups" && !options?.method) return response({ data: [] });
      if (path === "/admin/v1/model-deployments" && !options?.method) return response({ data: deployments });
      if (path.startsWith("/admin/v1/model-deployments/health?") && !options?.method) return response({ data: checks });
      if (path === "/admin/v1/model-groups" && options?.method === "POST") return response({});
      return response({});
    });
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    render(<MemoryRouter><AuthProvider><ModelGroupsPage /></AuthProvider></MemoryRouter>);

    await userEvent.click(await screen.findByRole("button", { name: "Create Model Group" }));
    const form = await screen.findByRole("dialog", { name: "Create model group" });
    await userEvent.type(within(form).getByLabelText("Public model"), "public-fast");
    const selector = within(form).getByLabelText("Deployments");
    await userEvent.click(selector);
    await userEvent.click(within(form).getByRole("option", { name: /primary/ }));
    await userEvent.type(selector, "fall");
    await userEvent.click(within(form).getByRole("option", { name: /fallback/ }));
    await userEvent.selectOptions(within(form).getByLabelText("Strategy"), "adaptive");
    await userEvent.type(within(form).getByLabelText("Retries unavailable"), "3");
    await userEvent.click(within(form).getByRole("button", { name: "Create group" }));

    await waitFor(() => expect(fetchMock.mock.calls.some(([path, options]) => String(path) === "/admin/v1/model-groups" && options?.method === "POST")).toBe(true));
    const create = fetchMock.mock.calls.find(([path, options]) => String(path) === "/admin/v1/model-groups" && options?.method === "POST")!;
    expect(JSON.parse(String(create[1]?.body))).toEqual({ id: "public-fast", deployment_ids: ["primary", "fallback"], strategy: "adaptive", retry_policy: { unavailable: 3 }, enabled: true });
  });
});
