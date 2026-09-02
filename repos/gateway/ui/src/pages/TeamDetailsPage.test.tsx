import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { useAuth } from "../auth/AuthContext";
import { useEffect, type ReactNode } from "react";
import { TeamDetailsPage } from "./TeamDetailsPage";

const json = (payload: unknown, status = 200) => Promise.resolve(new Response(status === 204 ? null : JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } }));

function renderPage() {
  sessionStorage.setItem("ai-gateway.admin-token", "token");
  return render(<MemoryRouter initialEntries={["/teams/platform"]}><AuthProvider><SessionGate><Routes><Route path="/teams/:id" element={<TeamDetailsPage />} /><Route path="/teams" element={<div>Teams list</div>} /></Routes></SessionGate></AuthProvider></MemoryRouter>);
}

function SessionGate({ children }: { children: ReactNode }) {
  const { session, restoreSession } = useAuth();
  useEffect(() => { if (!session) void restoreSession(); }, [restoreSession, session]);
  return session ? children : null;
}

describe("TeamDetailsPage", () => {
  afterEach(() => { vi.restoreAllMocks(); sessionStorage.clear(); });

  it("shows scoped members and keys and adds a configured user", async () => {
    const memberships = [{ team_id: "platform", user_id: "user-1", roles: ["team_admin"], created_at: "2026-09-01T10:00:00Z", updated_at: "2026-09-01T10:00:00Z" }];
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === "/admin/v1/session") return json({ roles: ["admin"], capabilities: ["admin", "team_directory"] });
      if (url.startsWith("/admin/v1/teams?")) return json({ data: [{ id: "platform", name: "Platform", description: "Core team", status: "active", member_count: memberships.length, created_at: "2026-08-01T10:00:00Z", updated_at: "2026-09-01T10:00:00Z" }] });
      if (url === "/admin/v1/teams/platform/members?limit=500" && (!init?.method || init.method === "GET")) return json({ data: memberships });
      if (url === "/admin/v1/users?limit=500") return json({ data: [{ id: "user-1", name: "Owner", email: "owner@example.test", status: "active" }, { id: "user-2", name: "Developer", email: "dev@example.test", status: "active" }] });
      if (url.startsWith("/admin/v1/keys?")) return json({ data: [{ id: "vk_platform", alias: "automation", status: "active", allowed_models: ["gpt-5"] }] });
      if (url === "/admin/v1/teams/platform/members/user-2" && init?.method === "PUT") { memberships.push({ team_id: "platform", user_id: "user-2", roles: ["developer"], created_at: "2026-09-02T10:00:00Z", updated_at: "2026-09-02T10:00:00Z" }); return json(memberships[1]); }
      return json({ error: { message: `unexpected ${init?.method || "GET"} ${url}` } }, 500);
    });
    renderPage();
    expect(await screen.findByRole("heading", { name: "Platform" })).toBeInTheDocument();
    expect(screen.getByText("owner@example.test")).toBeInTheDocument();
    expect(screen.getByText("automation")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Add member" }));
    await userEvent.selectOptions(screen.getByLabelText("Team member user"), "user-2");
    await userEvent.click(screen.getByRole("combobox", { name: "Team roles" }));
    await userEvent.click(within(screen.getByRole("listbox", { name: "Available team roles" })).getByRole("option", { name: /Developer/ }));
    await userEvent.click(screen.getByRole("button", { name: "Save membership" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, init]) => String(url).endsWith("/members/user-2") && init?.method === "PUT" && String(init.body).includes("developer"))).toBe(true));
    expect(await screen.findByText("dev@example.test")).toBeInTheDocument();
  });

  it("removes a member through the actions menu", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === "/admin/v1/session") return json({ roles: ["admin"], capabilities: ["admin", "team_directory"] });
      if (url.startsWith("/admin/v1/teams?")) return json({ data: [{ id: "platform", name: "Platform", status: "active", member_count: 1, created_at: "2026-08-01T10:00:00Z", updated_at: "2026-09-01T10:00:00Z" }] });
      if (url.endsWith("/members/user-1") && init?.method === "DELETE") return json({}, 204);
      if (url === "/admin/v1/teams/platform/members?limit=500") return json({ data: [{ team_id: "platform", user_id: "user-1", roles: ["member"], created_at: "2026-09-01T10:00:00Z", updated_at: "2026-09-01T10:00:00Z" }] });
      if (url === "/admin/v1/users?limit=500") return json({ data: [{ id: "user-1", name: "Owner", status: "active" }] });
      if (url.startsWith("/admin/v1/keys?")) return json({ data: [] });
      return json({ error: { message: "unexpected request" } }, 500);
    });
    renderPage();
    await screen.findByText("Owner");
    await userEvent.click(screen.getByRole("button", { name: "Actions for member user-1" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Remove" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, init]) => String(url).endsWith("/members/user-1") && init?.method === "DELETE")).toBe(true));
  });
});
