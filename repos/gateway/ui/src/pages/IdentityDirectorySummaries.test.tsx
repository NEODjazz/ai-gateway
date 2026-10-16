import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect, type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider, useAuth } from "../auth/AuthContext";
import { IdentityDirectoryPage } from "./IdentityDirectoryPage";

const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), {status,headers:{"Content-Type":"application/json"}});
function Gate({children}:{children:ReactNode}) { const {session,restoreSession}=useAuth(); useEffect(()=>{void restoreSession();},[restoreSession]); return session ? children : null; }
const identity = (id: string) => ({ id, name: `Name ${id}`, status: "active", member_count: 620 });
const session = {user_id:"operator",credential_id:"operator-key",roles:["admin"],capabilities:["admin","team_directory"]};
const from = "2026-09-15T12:00:00Z", to = "2026-10-15T12:00:00Z";
function summary(id: string) { return { id, directory:{id,key_count:71,organization:{id:"owner",name:"Organization owner",status:"active"}}, policy:{model_access:"allowlist",allowed_models:["model-a","model-b"],model_count:3,rpm:60,tpm:100000}, budgets:{id,total:60,data:[1,2].map((number)=>({policy:{id:number,scope_type:"team",scope_id:id,period:"day",currency:number===1?"USD":"EUR",max_cost:10,max_tokens:100,enabled:true},used_cost:number===1?3:4,used_tokens:30,window_start:"2026-10-15T00:00:00Z",window_end:"2026-10-16T00:00:00Z"}))},spend:[{id,currency:"USD",cost:2,requests:3},{id,currency:"EUR",cost:5,requests:2}]}; }
function envelope(id: string) {return {scope_type:"team",from,to,data:[summary(id)],errors:{}};}
function show() {sessionStorage.setItem("ai-gateway.admin-token","fixture");return render(<MemoryRouter><AuthProvider><Gate><IdentityDirectoryPage kind="teams"/></Gate></AuthProvider></MemoryRouter>);}

it("loads one bounded summary batch for the page and shows exact counts, separate currencies and budget windows",async()=>{
  const summaryQueries:unknown[]=[];
  vi.spyOn(globalThis,"fetch").mockImplementation(async(input,options)=>{
    const path=String(input); if(path.endsWith("/session"))return json(session);
    if(path.endsWith("/directory-summaries")){summaryQueries.push(JSON.parse(String(options?.body)));return json(envelope("late-620"));}
    return json({data:[identity("late-620")],total:130});
  });
  show();
  await screen.findByText("Organization owner");
  expect(summaryQueries).toEqual([{scope_type:"team",ids:["late-620"]}]);
  expect(screen.getByRole("link",{name:"71 keys"})).toHaveAttribute("href","/teams/late-620?tab=keys");
  expect(screen.getByRole("link",{name:"3 model patterns"})).toHaveAttribute("href","/teams/late-620?tab=policy");
  expect(screen.getByRole("link",{name:"620 members"})).toHaveAttribute("href","/teams/late-620?tab=members");
  expect(screen.getByText("$2.00")).toBeInTheDocument();expect(screen.getByText("€5.00")).toBeInTheDocument();
  expect(screen.getByText("+58 more policies")).toBeInTheDocument();
  expect(screen.getByText("$3.00 / $10.00")).toBeInTheDocument();
  expect(screen.getByText("€4.00 / €10.00")).toBeInTheDocument();
  expect(screen.getByText("1–25 of 130")).toBeInTheDocument();
  expect(screen.getByRole("columnheader",{name:"Spend (30d)"})).not.toContainElement(screen.queryByRole("button",{name:/Spend/}));
});

it("keeps directory and independent sources visible when budgets fail and distinguishes absence from zero",async()=>{
  vi.spyOn(globalThis,"fetch").mockImplementation(async(input)=>{
    const path=String(input);if(path.endsWith("/session"))return json(session);
    if(path.endsWith("/directory-summaries")){const value=envelope("a");return json({...value,errors:{budgets:"Budget service unavailable"},data:[{...value.data[0],budgets:null,spend:[]}]});}
    return json({data:[identity("a")],total:1});
  });show();
  await screen.findByText("Budget service unavailable");
  expect(screen.getByRole("link",{name:"Name a"})).toBeInTheDocument();
  expect(screen.getByRole("link",{name:"71 keys"})).toBeInTheDocument();
  expect(screen.getByText("No finalized usage")).toBeInTheDocument();
  expect(screen.queryByText("No direct budget")).not.toBeInTheDocument();
  expect(screen.getByText("Unavailable")).toBeInTheDocument();
});

it("ignores a delayed previous page summary and retains a marked successful read after refresh failure",async()=>{
  let resolveOld!:(response:Response)=>void;let fail=false;
  vi.spyOn(globalThis,"fetch").mockImplementation(async(input,options)=>{
    const path=String(input);if(path.endsWith("/session"))return json(session);
    if(path.endsWith("/directory-summaries")){
      const q=JSON.parse(String(options?.body));
      if(q.ids[0]==="old")return new Promise(resolve=>{resolveOld=resolve;});
      return fail?json({error:{message:"Summary refresh failed"}},503):json(envelope("fresh"));
    }
    const q=new URL(path,"http://example.test").searchParams;return json({data:[identity(q.get("offset")==="25"?"fresh":"old")],total:60});
  });show();await screen.findByRole("link",{name:"Name old"});
  await waitFor(()=>expect(resolveOld).toBeTypeOf("function"));
  await userEvent.click(screen.getByRole("button",{name:"Next"}));
  await screen.findByRole("link",{name:"Name fresh"});await screen.findByText("Organization owner");
  const stale=envelope("old");stale.data[0].directory.organization.name="Stale organization";
  await act(async()=>resolveOld(json(stale)));
  expect(screen.queryByText("Stale organization")).not.toBeInTheDocument();
  fail=true;await userEvent.click(screen.getByRole("button",{name:"Refresh summaries"}));
  await screen.findByText("Summary refresh failed");
  expect(screen.getByText("Summaries show the previous successful read.")).toBeInTheDocument();
  expect(screen.getByRole("link",{name:"71 keys"})).toHaveAttribute("href","/teams/fresh?tab=keys");
});

it("drops summaries on verified scope change and never fetches them for a team administrator",async()=>{
  let scoped=false; const queries:unknown[]=[];
  vi.spyOn(globalThis,"fetch").mockImplementation(async(input,options)=>{
    const path=String(input);if(path.endsWith("/session"))return json(scoped?{...session,user_id:"team-admin",credential_id:"team-key",team_id:"a",roles:["team_admin"],capabilities:["team_directory"]}:session);
    if(path.endsWith("/directory-summaries")){queries.push(JSON.parse(String(options?.body)));return json(envelope("a"));}
    return json({data:[identity("a")],total:1});
  });
  function Switch(){const{restoreSession}=useAuth();return <button onClick={()=>{scoped=true;void restoreSession();}}>Switch scope</button>;}
  sessionStorage.setItem("ai-gateway.admin-token","fixture");render(<MemoryRouter><AuthProvider><Switch/><Gate><IdentityDirectoryPage kind="teams"/></Gate></AuthProvider></MemoryRouter>);
  await screen.findByText("Organization owner");
  await userEvent.click(screen.getByRole("button",{name:"Switch scope"}));
  await screen.findByText("Directory scoped to team a");
  const table=screen.getByRole("table",{name:"Managed data table"});
  expect(within(table).queryByText("Organization owner")).not.toBeInTheDocument();
  expect(within(table).getAllByText("Restricted")).toHaveLength(3);
  expect(queries).toHaveLength(1);
});

it.each(["foreign identity","foreign budget","unsafe number"])("rejects %s without showing plausible summary values",async(kind)=>{
  vi.spyOn(globalThis,"fetch").mockImplementation(async(input)=>{
    const path=String(input);if(path.endsWith("/session"))return json(session);
    if(path.endsWith("/directory-summaries")){
      const value=envelope("a");
      if(kind==="foreign identity")value.data[0].id="foreign";
      if(kind==="foreign budget")value.data[0].budgets.data[0].policy.scope_id="foreign";
      if(kind==="unsafe number")value.data[0].directory.key_count=Number.MAX_SAFE_INTEGER+1;
      return json(value);
    }
    return json({data:[identity("a")],total:1});
  });show();
  await screen.findByText("Directory summaries returned invalid or mismatched data.");
  expect(screen.getByRole("link",{name:"Name a"})).toBeInTheDocument();
  expect(screen.queryByRole("link",{name:"71 keys"})).not.toBeInTheDocument();
  expect(screen.queryByText("No direct budget")).not.toBeInTheDocument();
});
