import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { OrganizationDetailsPage } from "./OrganizationDetailsPage";

const json = (payload: unknown, status = 200) => Promise.resolve(new Response(status === 204 ? null : JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } }));

describe("OrganizationDetailsPage", () => {
  afterEach(() => { vi.restoreAllMocks(); sessionStorage.clear(); });

  it("assigns configured unowned teams and removes an assignment", async () => {
    sessionStorage.setItem("ai-gateway.admin-token", "token");
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const teamIDs = ["team-a"];
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === "/admin/v1/organizations?limit=500") return json({ data: [{ id: "acme", name: "Acme", description: "Primary org", status: "active", team_ids: [...teamIDs], created_at: "2026-08-01T10:00:00Z", updated_at: "2026-09-01T10:00:00Z" }, { id: "other", name: "Other", status: "active", team_ids: ["team-c"], created_at: "2026-08-01T10:00:00Z", updated_at: "2026-09-01T10:00:00Z" }] });
      if (url === "/admin/v1/teams?limit=500") return json({ data: [{ id: "team-a", name: "Platform", status: "active", member_count: 2 }, { id: "team-b", name: "Research", status: "active", member_count: 1 }, { id: "team-c", name: "Owned elsewhere", status: "active", member_count: 1 }] });
      if (url.startsWith("/admin/v1/keys?")) return json({ data: [{ id: "vk_acme", alias: "automation", team_id: "team-a", status: "active", allowed_models: ["gpt-5"] }] });
      if (url.endsWith("/teams/team-b") && init?.method === "PUT") { teamIDs.push("team-b"); return json({ id: "acme", team_ids: [...teamIDs] }); }
      if (url.endsWith("/teams/team-a") && init?.method === "DELETE") { teamIDs.splice(teamIDs.indexOf("team-a"), 1); return json({}, 204); }
      return json({ error: { message: `unexpected ${init?.method || "GET"} ${url}` } }, 500);
    });
    render(<MemoryRouter initialEntries={["/organizations/acme"]}><AuthProvider><Routes><Route path="/organizations/:id" element={<OrganizationDetailsPage />} /><Route path="/organizations" element={<div>Organization list</div>} /></Routes></AuthProvider></MemoryRouter>);
    expect(await screen.findByRole("heading", { name: "Acme" })).toBeInTheDocument();
    expect(screen.getByText("Platform")).toBeInTheDocument();
    expect(screen.getByText("automation")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Assign team" }));
    expect(screen.queryByRole("option", { name: /Owned elsewhere/ })).not.toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Organization team"), "team-b");
    await userEvent.click(within(screen.getByRole("dialog", { name: "Assign team" })).getByRole("button", { name: "Assign team" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, init]) => String(url).endsWith("/teams/team-b") && init?.method === "PUT")).toBe(true));
    expect(await screen.findByText("Research")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Actions for organization team team-a" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Remove" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, init]) => String(url).endsWith("/teams/team-a") && init?.method === "DELETE")).toBe(true));
  });
});
