import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import type { ReactNode } from "react";
import { AuthProvider } from "../auth/AuthContext";
import { ProjectDetailsPage, ProjectsPage } from "./ProjectsPage";

const json = (payload: unknown, status = 200) => Promise.resolve(new Response(status === 204 ? null : JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } }));

function authenticated(element: ReactNode, entry = "/projects") {
  sessionStorage.setItem("ai-gateway.admin-token", "project-token");
  return render(<MemoryRouter initialEntries={[entry]}><AuthProvider>{element}</AuthProvider></MemoryRouter>);
}

describe("ProjectsPage", () => {
  afterEach(() => { vi.restoreAllMocks(); sessionStorage.clear(); });

  it("joins owner teams and access groups and creates a project from configured teams", async () => {
    const projects = [{ id: "payments", name: "Payments", description: "Payment APIs", team_id: "platform", tags: ["prod"], enabled: true }];
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === "/admin/v1/projects" && (!init?.method || init.method === "GET")) return json({ data: projects });
      if (url === "/admin/v1/teams?limit=500") return json({ data: [{ id: "platform", name: "Platform", status: "active" }, { id: "risk", name: "Risk", status: "active" }] });
      if (url === "/admin/v1/access-groups") return json({ data: [{ id: "payments-read", project_id: "payments", name: "Readers", enabled: true }] });
      if (url === "/admin/v1/projects/risk-tools" && init?.method === "PUT") return json({ id: "risk-tools", ...JSON.parse(String(init.body)) });
      return json({ error: { message: `unexpected ${init?.method || "GET"} ${url}` } }, 500);
    });
    authenticated(<ProjectsPage />);
    expect(await screen.findByText("Payments")).toBeInTheDocument();
    expect(screen.getByText("Platform")).toBeInTheDocument();
    expect(screen.queryByText("payments-read")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Create Project" }));
    await userEvent.type(screen.getByLabelText("Project ID"), "risk-tools");
    await userEvent.type(screen.getByLabelText("Name"), "Risk tools");
    await userEvent.selectOptions(screen.getByLabelText("Owner team"), "risk");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, init]) => String(url) === "/admin/v1/projects/risk-tools" && init?.method === "PUT")).toBe(true));
    const create = fetchMock.mock.calls.find(([url, init]) => String(url) === "/admin/v1/projects/risk-tools" && init?.method === "PUT")!;
    expect(JSON.parse(String(create[1]?.body))).toMatchObject({ name: "Risk tools", team_id: "risk", enabled: true });
    expect(String(create[1]?.body)).not.toContain('"id"');
  });

  it("shows a project workspace with effective grants and deduplicated linked keys", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      if (url === "/admin/v1/projects/payments") return json({ id: "payments", name: "Payments", description: "Payment APIs", team_id: "platform", tags: ["prod"], enabled: true });
      if (url === "/admin/v1/teams?limit=500") return json({ data: [{ id: "platform", name: "Platform", status: "active" }] });
      if (url === "/admin/v1/access-groups") return json({ data: [
        { id: "payments-read", project_id: "payments", name: "Readers", allowed_models: ["gpt-5"], allowed_tools: ["ledger"], enabled: true },
        { id: "payments-write", project_id: "payments", name: "Writers", allowed_models: ["gpt-5", "gpt-5-mini"], allowed_tools: ["ledger"], enabled: true },
        { id: "other", project_id: "other", name: "Other", enabled: true }
      ] });
      if (url === "/admin/v1/keys?limit=500&status=non_revoked&sort_by=alias&sort_order=asc&access_group_id=payments-read&access_group_id=payments-write") return json({ data: [
        { id: "vk-payment", alias: "payment-agent", status: "active", team_id: "platform", access_group_ids: ["payments-read", "payments-write"], allowed_models: ["gpt-5"] },
        { id: "vk-other", alias: "other-agent", status: "active", access_group_ids: ["other"] }
      ], total: 2 });
      return json({ error: { message: `unexpected ${url}` } }, 500);
    });
    authenticated(<Routes><Route path="/projects/:id" element={<ProjectDetailsPage />} /><Route path="/projects" element={<div>Projects list</div>} /></Routes>, "/projects/payments");
    expect(await screen.findByRole("heading", { name: "Payments" })).toBeInTheDocument();
    expect(screen.getByText("Readers")).toBeInTheDocument();
    expect(screen.getByText("Writers")).toBeInTheDocument();
    expect(screen.getByText("payment-agent")).toBeInTheDocument();
    expect(screen.queryByText("other-agent")).not.toBeInTheDocument();
    expect(screen.getAllByText("gpt-5").length).toBeGreaterThan(0);
    expect(screen.getByText(/project ID is not sent to providers/i)).toBeInTheDocument();
  });
});
