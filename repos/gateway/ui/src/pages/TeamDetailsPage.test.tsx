import { useEffect, type ReactNode } from "react";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Link, MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { AuthProvider, useAuth } from "../auth/AuthContext";
import { TeamDetailsPage, UserDetailsPage } from "./TeamDetailsPage";
import type { DirectoryMembership } from "./directory/MembershipsPanel";

const time = "2026-09-01T10:00:00Z";
const json = (value: unknown, status = 200) => new Response(status === 204 ? null : JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const member = (user = "user-1", name = "Owner"): DirectoryMembership => ({ team_id: "platform", user_id: user, roles: ["member"], user_name: name, user_email: `${user}@example.test`, user_status: "active", team_name: "Platform", team_status: "active", created_at: time, updated_at: time });
function Gate({ children }: { children: ReactNode }) { const { session, restoreSession } = useAuth(); useEffect(() => { if (!session) void restoreSession(); }, [session, restoreSession]); return session ? children : null; }
function LocationProbe() { const location = useLocation(); return <div>Policy route {location.search}</div>; }
function show(user = false) {
  sessionStorage.setItem("ai-gateway.admin-token", "fixture");
  return render(<MemoryRouter initialEntries={[user ? "/users/user-1" : "/teams/platform"]}><AuthProvider><Gate><Routes><Route path="/teams/:id" element={<TeamDetailsPage />} /><Route path="/users/:id" element={<UserDetailsPage />} /><Route path="/policies" element={<LocationProbe />} /></Routes></Gate></AuthProvider></MemoryRouter>);
}
function fixture({ scoped = false, keyFailure = false, deleteFailure = false, count = 1 } = {}) {
  const members = Array.from({ length: count }, (_, i) => member(`user-${i + 1}`, i === 0 ? "Owner" : i === count - 1 ? "Late user" : `Member ${i + 1}`));
  const requests: { path: string; method?: string; body?: unknown }[] = [];
  const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
    const path = String(input); const url = new URL(path, "http://example.test"); const q = url.searchParams;
    requests.push({ path, method: options?.method, body: options?.body ? JSON.parse(String(options.body)) : undefined });
    if (url.pathname === "/admin/v1/session") return json({ user_id: "operator", team_id: scoped ? "platform" : undefined, roles: [scoped ? "team_admin" : "admin"], capabilities: scoped ? ["team_directory"] : ["admin", "team_directory"] });
    if (url.pathname === "/admin/v1/teams/platform") return json({id:"platform",name:"Platform",description:"Core team",status:"active",member_count:members.length,external_id:"external-team",created_at:time,updated_at:time});
    if (url.pathname === "/admin/v1/teams" && q.get("team_id")) return json({ data: [{ id: "platform", name: "Platform", description: "Core team", status: "active", member_count: members.length, external_id: "external-team", created_at: time, updated_at: time }], total: 1, offset: 0, limit: 1 });
    if (url.pathname === "/admin/v1/teams") return json({ data: [{id:"platform",name:"Platform",status:"active"},{id:"research",name:"Research",status:"active"}],total:2,offset:0,limit:25 });
    if (url.pathname === "/admin/v1/users/user-1") return json({ id: "user-1", name: "Owner", email: "owner@example.test", roles: ["user"], team_ids: members.filter(row => row.user_id === "user-1").map(row => row.team_id), external_id: "external-owner", status: "active", created_at: time, updated_at: time });
    if (url.pathname === "/admin/v1/users") return json({ data: [{ id: "user-1", name: "Owner", status: "active" }, { id: "user-2", name: "Developer", status: "active" }], total: 2, offset: Number(q.get("offset")), limit: 25 });
    if (url.pathname === "/admin/v1/team-memberships") {
      const filtered = members.filter((row) => (!q.get("team_id") || row.team_id === q.get("team_id")) && (!q.get("user_id") || row.user_id === q.get("user_id")) && JSON.stringify(row).toLowerCase().includes((q.get("search") || "").toLowerCase()));
      const offset = Number(q.get("offset")), limit = Number(q.get("limit"));
      return json({ data: filtered.slice(offset, offset + limit), total: filtered.length, offset, limit });
    }
    if (url.pathname === "/admin/v1/budgets/page") return json({data:[],total:0,offset:0,limit:25});
    if (url.pathname === "/admin/v1/keys") {
      if (keyFailure) return json({ error: { message: "Key inventory unavailable" } }, 503);
      return json({ data: [{ id: "vk_platform", alias: "automation", user_id: "user-1", team_id: "platform", status: "active", allowed_models: ["gpt-5"], created_at: time }], total: 71, offset: Number(q.get("offset")), limit: Number(q.get("limit")) });
    }
    if (/^\/admin\/v1\/teams\/(platform|research)\/members$/.test(url.pathname) && options?.method === "POST") {
      const body = JSON.parse(String(options.body));
      const team = url.pathname.includes("/research/") ? "research" : "platform";
      if (members.some((row) => row.user_id === body.user_id && row.team_id === team)) return json({ error: { message: "Membership already exists" } }, 409);
      const added = { ...member(body.user_id, body.user_id === "user-1" ? "Owner" : "Developer"), team_id: team, team_name: team === "research" ? "Research" : "Platform", roles: body.roles }; members.push(added); return json(added, 201);
    }
    if (url.pathname.startsWith("/admin/v1/teams/platform/members/") && options?.method === "PUT") {
      const row = members.find(item => item.user_id === url.pathname.split("/").at(-1));
      if (!row) return json({error:{message:"Not found"}},404);
      row.roles = JSON.parse(String(options.body)).roles; return json(row);
    }
    if (url.pathname.startsWith("/admin/v1/teams/platform/members/") && options?.method === "DELETE") {
      if (deleteFailure) return json({ error: { message: "Membership removal failed" } }, 503);
      const id = url.pathname.split("/").at(-1); members.splice(members.findIndex((row) => row.user_id === id), 1); return json({}, 204);
    }
    return json({ error: { message: `Unexpected ${options?.method || "GET"} ${path}` } }, 500);
  });
  return { members, requests, fetch };
}

describe("Identity workspaces", () => {
  it("shows independent team members and scoped keys and preserves policy actions", async () => {
    fixture(); show();
    expect(await screen.findByRole("heading", { name: "Platform" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Owner" })).toHaveAttribute("href", "/users/user-1");
    expect(await screen.findByText("automation")).toBeInTheDocument();
    expect(screen.getByText("1–25 of 71")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Policy actions for Platform" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Simulate policy impact" }));
    expect(await screen.findByText("Policy route ?view=simulator&team_id=platform")).toBeInTheDocument();
  });
  it("searches members beyond 500 and keeps the team count independent from the filtered page", async () => {
    const { requests } = fixture({ count: 620 }); show();
    await screen.findByRole("link", { name: "Owner" });
    expect(screen.getByText("1–25 of 620")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Members" }));
    await userEvent.type(screen.getByRole("searchbox", { name: "Search team members" }), "Late user");
    expect(await screen.findByRole("link", { name: "Late user" })).toHaveAttribute("href", "/users/user-620");
    expect(screen.getByText("1–1 of 1")).toBeInTheDocument();
    expect(requests.some((request) => request.path.includes("search=Late+user"))).toBe(true);
    expect(requests.some((request) => request.path.includes("limit=500"))).toBe(false);
  });
  it("adds a configured user through create-only membership and retains errors without overwriting roles", async () => {
    const { requests } = fixture(); show(); await screen.findByRole("link", { name: "Owner" });
    await userEvent.click(screen.getByRole("button", { name: "Add member" }));
    const dialog = screen.getByRole("dialog", { name: "Add team member" });
    await userEvent.selectOptions(await screen.findByLabelText("Team member user"), "user-1");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save membership" }));
    expect(await screen.findByText("Membership already exists")).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Team member user"), "user-2");
    await userEvent.click(within(dialog).getByRole("combobox", { name: "Team roles" }));
    await userEvent.click(within(screen.getByRole("listbox", { name: "Available team roles" })).getByRole("option", { name: /^Developer/ }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Save membership" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(await screen.findByRole("link", { name: "Developer" })).toBeInTheDocument();
    expect(requests.some((request) => request.path.endsWith("/platform/members") && request.method === "POST" && JSON.stringify(request.body).includes("user-2"))).toBe(true);
    expect(requests.some((request) => request.method === "PUT")).toBe(false);
  });
  it("removes scoped members, surfaces failures and never loads global keys for a team admin", async () => {
    const { requests } = fixture({ scoped: true, deleteFailure: true });
    vi.spyOn(window, "confirm").mockReturnValue(true);
    show(); await screen.findByRole("link", { name: "Owner" });
    expect(screen.queryByRole("button", { name: "Policy actions for Platform" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Actions for member user-1" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Remove" }));
    expect(await screen.findByText("Membership removal failed")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Owner" })).toBeInTheDocument();
    expect(requests.some((request) => /keys\?|budgets|usage\?/.test(request.path))).toBe(false);
  });
  it("edits membership roles independently of global approvals", async () => {
    const {requests}=fixture({scoped:true});show(); await screen.findByRole("link",{name:"Owner"});
    await userEvent.click(screen.getByRole("button",{name:"Actions for member user-1"}));
    await userEvent.click(screen.getByRole("menuitem",{name:"Edit roles"}));
    const dialog=screen.getByRole("dialog",{name:"Edit team member"});
    await userEvent.click(within(dialog).getByRole("combobox",{name:"Team roles"}));
    await userEvent.click(within(screen.getByRole("listbox",{name:"Available team roles"})).getByRole("option",{name:/^Team admin/}));
    await userEvent.click(within(dialog).getByRole("button",{name:"Save membership"}));
    await waitFor(()=>expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(await screen.findByText("team_admin")).toBeInTheDocument();
    expect(requests.some(r=>r.path.endsWith("/teams/platform/members/user-1")&&r.method==="PUT"&&JSON.stringify(r.body).includes("team_admin"))).toBe(true);
    expect(requests.some(r=>r.path.includes("/users/")&&r.method==="PUT")).toBe(false);
  });
  it("does not add a default role when editing an empty membership",async()=>{
    const {members,requests}=fixture();delete members[0].roles;show();await screen.findByRole("link",{name:"Owner"});
    await userEvent.click(screen.getByRole("button",{name:"Actions for member user-1"}));
    await userEvent.click(screen.getByRole("menuitem",{name:"Edit roles"}));
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button",{name:"Save membership"}));
    await waitFor(()=>expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(requests.find(r=>r.method==="PUT")?.body).toEqual({roles:[]});
  });
  it("rejects membership metadata from a different team without hiding the profile",async()=>{
    vi.spyOn(globalThis,"fetch").mockImplementation(async input=>{
      const path=String(input),q=new URL(path,"http://example.test").searchParams;
      if(path.endsWith("/session"))return json({roles:["admin"],capabilities:["admin","team_directory"]});
      if(path.endsWith("/teams/platform"))return json({id:"platform",name:"Platform",status:"active",member_count:1,created_at:time,updated_at:time});
      if(path.includes("team-memberships?"))return json({data:[{...member("foreign","Private user"),team_id:"foreign-team"}],total:1,offset:0,limit:25});
      return json({data:[],total:0,offset:Number(q.get("offset")),limit:Number(q.get("limit"))});
    });
    show();expect(await screen.findByText("The directory returned invalid data.")).toBeInTheDocument();
    expect(screen.getByRole("heading",{name:"Platform"})).toBeInTheDocument();
    expect(screen.queryByText("Private user")).not.toBeInTheDocument();
  });
  it("removes a membership and refreshes the scoped view",async()=>{
    fixture({scoped:true});vi.spyOn(window,"confirm").mockReturnValue(true);show();await screen.findByRole("link",{name:"Owner"});
    await userEvent.click(screen.getByRole("button",{name:"Actions for member user-1"}));
    await userEvent.click(screen.getByRole("menuitem",{name:"Remove"}));
    expect(await screen.findByText("0 results")).toBeInTheDocument();
    expect(screen.queryByRole("link",{name:"Owner"})).not.toBeInTheDocument();
  });
  it("adds a team from the user workspace through the same create-only membership API",async()=>{
    const {requests}=fixture();show(true);await screen.findByRole("link",{name:"Platform"});
    await userEvent.click(screen.getByRole("button",{name:"Add team"}));
    const dialog=screen.getByRole("dialog",{name:"Add user to team"});
    await userEvent.selectOptions(await screen.findByLabelText("Membership team"),"research");
    await userEvent.click(within(dialog).getByRole("button",{name:"Save membership"}));
    expect(await screen.findByRole("link",{name:"Research"})).toHaveAttribute("href","/teams/research");
    expect(requests.some(r=>r.path.endsWith("/teams/research/members")&&r.method==="POST"&&JSON.stringify(r.body).includes("user-1"))).toBe(true);
  });
  it("does not hide the profile or members when keys fail", async () => {
    fixture({ keyFailure: true }); show();
    expect(await screen.findByText("Key inventory unavailable")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Platform" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Owner" })).toBeInTheDocument();
  });
  it("shows reverse user memberships, filtered key inventory and immutable identity settings", async () => {
    const { requests } = fixture(); show(true);
    expect(await screen.findByRole("heading", { name: "Owner" })).toBeInTheDocument();
    expect(screen.getAllByText("2026-09-01")).toHaveLength(2);
    expect(screen.getAllByText("10:00:00 UTC")).toHaveLength(2);
    expect(await screen.findByRole("link", { name: "Platform" })).toHaveAttribute("href", "/teams/platform");
    expect(requests.some((request) => request.path.includes("team-memberships?user_id=user-1"))).toBe(true);
    expect(requests.some((request) => request.path.includes("keys?user_id=user-1"))).toBe(true);
    await userEvent.click(screen.getByRole("tab", { name: "Settings" }));
    expect(screen.getByText("external-owner")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Edit settings" }));
    expect(within(screen.getByRole("dialog")).getByLabelText("ID")).toHaveAttribute("readonly");
    expect(within(screen.getByRole("dialog")).getByLabelText("External ID")).toHaveValue("external-owner");
  });
  it("ignores an old user profile response after navigating to a different identity", async () => {
    let resolveOld!: (response: Response) => void;
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const path = String(input);
      if (path.endsWith("/session")) return json({ roles: ["admin"], capabilities: ["admin", "team_directory"] });
      if (path.endsWith("/users/old")) return new Promise((resolve) => { resolveOld = resolve; });
      if (path.endsWith("/users/new")) return json({ id: "new", name: "New identity", status: "active", roles: ["user"], team_ids: [], created_at: time, updated_at: time });
      const q = new URL(path, "http://example.test").searchParams;
      return json({ data: [], total: 0, offset: Number(q.get("offset")), limit: Number(q.get("limit")) });
    });
    sessionStorage.setItem("ai-gateway.admin-token", "fixture");
    render(<MemoryRouter initialEntries={["/users/old"]}><AuthProvider><Gate><Link to="/users/new">Next user</Link><Routes><Route path="/users/:id" element={<UserDetailsPage />} /></Routes></Gate></AuthProvider></MemoryRouter>);
    await waitFor(() => expect(resolveOld).toBeTypeOf("function"));
    await userEvent.click(screen.getByRole("link", { name: "Next user" }));
    await screen.findByRole("heading", { name: "New identity" });
    resolveOld(json({ id: "old", name: "Old identity", status: "active" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "New identity" })).toBeInTheDocument());
    expect(screen.queryByText("Old identity")).not.toBeInTheDocument();
  });
});
