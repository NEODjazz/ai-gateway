import { useEffect, useState, type ReactNode } from "react";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider, useAuth } from "../../auth/AuthContext";
import type { BudgetSummary } from "../budgets/BudgetForm";
import { IdentityBudgetsPanel } from "./IdentityBudgetsPanel";

const time = "2026-10-01T00:00:00Z";
function summary(id = 1, scope = "platform", kind = "team"): BudgetSummary {
  return { policy: { id, scope_type: kind, scope_id: scope, period: "month", currency: "USD", max_cost: 10, max_tokens: 100, enabled: true, created_at: time, updated_at: time }, window_start: time, window_end: "2026-11-01T00:00:00Z", used_cost: 2, remaining_cost: 8, used_tokens: 100, remaining_tokens: 0 };
}
const json = (value: unknown, status = 200) => new Response(status === 204 ? null : JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
function Gate({ children }: { children: ReactNode }) { const { session, restoreSession } = useAuth(); useEffect(() => { if (!session) void restoreSession(); }, [session, restoreSession]); return session ? children : null; }
function show(kind: "team" | "user" = "team", changeable = false) {
  function Workspace() { const [id, setID] = useState("platform"); return <>{changeable && <button onClick={() => setID("research")}>Other identity</button>}<IdentityBudgetsPanel kind={kind} id={id} /></>; }
  sessionStorage.setItem("ai-gateway.admin-token", "fixture");
  return render(<MemoryRouter><AuthProvider><Gate><Workspace /></Gate></AuthProvider></MemoryRouter>);
}
type Request = { url: URL; method: string; body?: unknown };
function fixture(handler?: (request: Request) => Response | Promise<Response> | undefined, scoped = false) {
  const requests: Request[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
    const url = new URL(String(input), "http://example.test");
    const request = { url, method: options?.method || "GET", body: options?.body ? JSON.parse(String(options.body)) : undefined }; requests.push(request);
    if (url.pathname === "/admin/v1/session") return json({ user_id: "operator", roles: [scoped ? "team_admin" : "admin"], capabilities: scoped ? ["team_directory"] : ["admin"] });
    const custom = handler?.(request); if (custom) return custom;
    if (url.pathname === "/admin/v1/budgets/page") return json({ data: [summary(1, url.searchParams.get("scope_id")!, url.searchParams.get("scope_type")!)], total: 1, offset: Number(url.searchParams.get("offset")), limit: 25 });
    return json({}, 201);
  });
  return requests;
}
describe("IdentityBudgetsPanel", () => {
  afterEach(() => { vi.restoreAllMocks(); sessionStorage.clear(); });
  it("pages all direct policies with exact totals, current currency usage and a real zero remainder", async () => {
    const requests = fixture(({url}) => {
      if (url.pathname !== "/admin/v1/budgets/page") return;
      const offset = Number(url.searchParams.get("offset"));
      return json({ data: Array.from({length:51},(_,i)=>summary(i+1)).slice(offset,offset+25), total:51,offset,limit:25 });
    });
    show(); await screen.findByRole("link", { name: "#1" });
    expect(screen.getByText("1–25 of 51")).toBeInTheDocument();
    expect(screen.getAllByText("$2.00 / $10.00")).toHaveLength(25);
    expect(screen.getAllByText(/0 tokens$/)).toHaveLength(25);
    await userEvent.click(screen.getByRole("button",{name:"Next budgets"}));
    await screen.findByRole("link", { name: "#26" });
    await userEvent.click(screen.getByRole("button",{name:"Next budgets"}));
    await screen.findByRole("link", { name: "#51" });
    expect(screen.getByText("51–51 of 51")).toBeInTheDocument();
    expect(screen.getByRole("button",{name:"Next budgets"})).toBeDisabled();
    expect(requests.filter(r=>r.url.pathname==="/admin/v1/budgets/page").every(r=>r.url.searchParams.get("scope_type")==="team"&&r.url.searchParams.get("scope_id")==="platform")).toBe(true);
  });
  it("pins user creation to the workspace, preserves failed drafts and prevents closing or resubmitting while saving", async () => {
    let resolveSave!: (response: Response) => void;
    let attempts = 0;
    const requests = fixture(({url,method}) => {
      if (url.pathname === "/admin/v1/budgets" && method === "POST") { attempts++; return attempts === 1 ? json({error:{message:"Billing unavailable"}},503) : new Promise<Response>(resolve=>{resolveSave=resolve;}); }
    });
    show("user"); await screen.findByRole("link",{name:"#1"});
    await userEvent.click(screen.getByRole("button",{name:"Create budget"}));
    const dialog=screen.getByRole("dialog",{name:"Create budget"});
    expect(within(dialog).getByText("user · platform")).toBeInTheDocument();
    expect(within(dialog).queryByLabelText("Budget scope type")).not.toBeInTheDocument();
    await userEvent.type(within(dialog).getByLabelText("Budget maximum tokens"),"5000");
    await userEvent.click(within(dialog).getByRole("button",{name:"Create budget"}));
    expect(await within(dialog).findByText("Billing unavailable")).toBeInTheDocument();
    expect(within(dialog).getByLabelText("Budget maximum tokens")).toHaveValue(5000);
    await userEvent.click(within(dialog).getByRole("button",{name:"Create budget"}));
    await userEvent.keyboard("{Escape}");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(within(dialog).getByRole("button",{name:"Saving…"})).toBeDisabled();
    expect(attempts).toBe(2);
    resolveSave(json({},201));
    await waitFor(()=>expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    const mutations=requests.filter(r=>r.method==="POST");
    expect(mutations[0].body).toEqual({scope_type:"user",scope_id:"platform",period:"month",currency:"USD",enabled:true,max_tokens:5000});
    expect(requests.some(r=>r.url.pathname==="/admin/v1/users"||r.url.pathname==="/admin/v1/teams")).toBe(false);
  });
  it("edits actual caps and reports disable failures without hiding usage", async () => {
    const requests=fixture(({method})=>method==="DELETE"?json({error:{message:"Disable failed"}},503):undefined);
    vi.spyOn(window,"confirm").mockReturnValue(true);
    show();await screen.findByRole("link",{name:"#1"});
    await userEvent.click(screen.getByRole("button",{name:"Edit budget 1"}));
    const dialog=screen.getByRole("dialog",{name:"Edit budget"});
    await userEvent.clear(within(dialog).getByLabelText("Budget maximum cost"));
    await userEvent.type(within(dialog).getByLabelText("Budget maximum cost"),"20");
    await userEvent.click(within(dialog).getByRole("button",{name:"Save changes"}));
    await waitFor(()=>expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(requests.find(r=>r.method==="PUT")?.body).toMatchObject({scope_type:"team",scope_id:"platform",max_cost:20,max_tokens:100});
    await userEvent.click(screen.getByRole("button",{name:"Disable budget 1"}));
    expect(await screen.findByText("Disable failed")).toBeInTheDocument();
    expect(screen.getByText("$2.00 / $10.00")).toBeInTheDocument();
  });
  it("does not load or expose financial controls to a scoped administrator", async () => {
    const requests=fixture(undefined,true);show();
    await screen.findByText("Financial controls are restricted to global administrators.");
    expect(requests.some(r=>r.url.pathname.includes("budgets"))).toBe(false);
    expect(screen.queryByRole("button",{name:"Create budget"})).not.toBeInTheDocument();
  });
  it("rejects a policy belonging to a different identity", async () => {
    fixture(({url})=>url.pathname==="/admin/v1/budgets/page"?json({data:[summary(999,"foreign")],total:1,offset:0,limit:25}):undefined);show();
    await screen.findByText("The directory returned invalid data.");
    expect(screen.queryByRole("link",{name:"#999"})).not.toBeInTheDocument();
    expect(screen.queryByText("0 budgets")).not.toBeInTheDocument();
  });
  it("retains the last successful data on failed refresh", async () => {
    let fail=false;
    fixture(({url})=>url.pathname==="/admin/v1/budgets/page"&&fail?json({error:{message:"Summaries unavailable"}},503):undefined);show();
    await screen.findByRole("link",{name:"#1"});fail=true;
    await userEvent.click(screen.getByRole("button",{name:"Refresh budgets"}));
    await screen.findByText("Summaries unavailable");
    expect(screen.getByRole("link",{name:"#1"})).toBeInTheDocument();
    expect(screen.getByText("$2.00 / $10.00")).toBeInTheDocument();
  });
  it("ignores a delayed response after changing identity", async () => {
    let resolveOld!:(response:Response)=>void;
    const requests=fixture(({url})=>url.pathname==="/admin/v1/budgets/page"&&url.searchParams.get("scope_id")==="platform"?new Promise<Response>(resolve=>{resolveOld=resolve;}):undefined);
    show("team",true);
    await waitFor(()=>expect(requests.some(r=>r.url.pathname==="/admin/v1/budgets/page")).toBe(true));
    await userEvent.click(screen.getByRole("button",{name:"Other identity"}));
    await screen.findByRole("link",{name:"#1"});
    resolveOld(json({data:[summary(999)],total:1,offset:0,limit:25}));
    await waitFor(()=>expect(screen.queryByRole("link",{name:"#999"})).not.toBeInTheDocument());
    expect(screen.getByRole("link",{name:"#1"})).toBeInTheDocument();
  });
});
