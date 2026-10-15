import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { useEffect, type ReactNode } from "react";
import { AuthProvider, useAuth } from "../auth/AuthContext";
import { IdentityDirectoryPage } from "./IdentityDirectoryPage";

const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), {status,headers:{"Content-Type":"application/json"}});
function Gate({children}:{children:ReactNode}) { const {session,restoreSession}=useAuth(); useEffect(()=>{void restoreSession();},[restoreSession]); return session ? children : null; }
function show(kind:"users"|"teams"="users") { sessionStorage.setItem("ai-gateway.admin-token","fixture"); return render(<MemoryRouter><AuthProvider><Gate><IdentityDirectoryPage kind={kind}/></Gate></AuthProvider></MemoryRouter>); }
const session = {roles:["admin"],capabilities:["admin","team_directory"],user_id:"operator"};
const record = {id:"u-1",name:"Alice",status:"active",roles:["user"],team_ids:["platform"]};

describe("IdentityDirectoryPage",()=>{
  it("uses backend totals and searches/sorts beyond the first page",async()=>{
    const paths:string[]=[];
    vi.spyOn(globalThis,"fetch").mockImplementation(async(input)=>{
      const path=String(input); paths.push(path);
      if(path.endsWith("/session")) return json(session);
      const q=new URL(path,"http://example.test").searchParams;
      return json({data:[{...record,id:q.get("offset")==="25"?"u-26":"u-1",name:q.get("search")||"Alice"}],total:130});
    });
    show();
    expect(await screen.findByText("1–25 of 130")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button",{name:"Next"}));
    expect(await screen.findByText("u-26")).toBeInTheDocument();
    expect(screen.getByText("26–50 of 130")).toBeInTheDocument();
    await userEvent.type(screen.getByRole("searchbox"),"Beyond");
    await waitFor(()=>expect(paths.some(path=>path.includes("search=Beyond")&&path.includes("offset=0"))).toBe(true));
    await screen.findByText("Beyond");
    await userEvent.selectOptions(screen.getByLabelText("Status"),"disabled");
    await userEvent.selectOptions(screen.getByLabelText("Approved role"),"developer");
    await waitFor(()=>expect(paths.some(path=>path.includes("status=disabled")&&path.includes("role=developer")&&path.includes("include_deleted=false"))).toBe(true));
    await userEvent.click(screen.getByRole("button",{name:/^Email/}));
    await waitFor(()=>expect(paths.some(path=>path.includes("sort_by=userName"))).toBe(true));
  });

  it("ignores late pages and preserves the committed page on failure",async()=>{
    let resolveOld!:(value:Response)=>void;
    vi.spyOn(globalThis,"fetch").mockImplementation(async(input)=>{
      const path=String(input);
      if(path.endsWith("/session"))return json(session);
      const q=new URL(path,"http://example.test").searchParams;
      if(q.get("search")==="x")return new Promise(resolve=>{resolveOld=resolve;});
      if(q.get("offset")==="25")return json({error:{message:"Directory unavailable"}},503);
      return json({data:[{...record,name:q.get("search")==="xy"?"Fresh identity":"Alice"}],total:130});
    });
    show(); await screen.findByText("Alice");
    await userEvent.type(screen.getByRole("searchbox"),"x");
    await waitFor(()=>expect(resolveOld).toBeTypeOf("function"));
    await userEvent.type(screen.getByRole("searchbox"),"y");
    await screen.findByText("Fresh identity");
    resolveOld(json({data:[{...record,name:"Stale identity"}],total:999}));
    await userEvent.click(screen.getByRole("button",{name:"Next"}));
    expect(await screen.findByText("Directory unavailable")).toBeInTheDocument();
    expect(screen.getByText("Fresh identity")).toBeInTheDocument();
    expect(screen.getByText("1–25 of 130")).toBeInTheDocument();
    expect(screen.queryByText("Stale identity")).not.toBeInTheDocument();
    expect(screen.queryByText(/of 999/)).not.toBeInTheDocument();
  });

  it("creates users with structured roles and retains drafts after conflicts",async()=>{
    const writes:{path:string;method?:string;body?:unknown}[]=[];
    let conflict=true;
    vi.spyOn(globalThis,"fetch").mockImplementation(async(input,options)=>{
      const path=String(input);
      if(path.endsWith("/session"))return json(session);
      if(options?.method==="POST") {
        writes.push({path,method:options.method,body:JSON.parse(String(options.body))});
        return conflict?json({error:{message:"Identity already exists"}},409):json(record,201);
      }
      return json({data:[record],total:1});
    });
    show(); await screen.findByText("Alice");
    await userEvent.click(screen.getByRole("button",{name:"Create User"}));
    const dialog=screen.getByRole("dialog",{name:"Create user"});
    await userEvent.clear(within(dialog).getByLabelText("ID")); await userEvent.type(within(dialog).getByLabelText("ID"),"new-user");
    await userEvent.type(within(dialog).getByLabelText("Email"),"new@example.test");
    expect(within(dialog).getByRole("combobox",{name:"Approved global roles"})).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button",{name:"Create user"}));
    expect(await screen.findByText("Identity already exists")).toBeInTheDocument();
    expect(within(dialog).getByLabelText("Email")).toHaveValue("new@example.test");
    expect(writes[0]).toMatchObject({path:"/admin/v1/users",method:"POST",body:{id:"new-user",roles:["user"],status:"active"}});
    conflict=false;
    await userEvent.click(within(dialog).getByRole("button",{name:"Create user"}));
    await waitFor(()=>expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(await screen.findByText("User new-user created.")).toBeInTheDocument();
  });

  it("limits a team administrator to team edits and read-only users",async()=>{
    const paths:string[]=[];
    vi.spyOn(globalThis,"fetch").mockImplementation(async(input)=>{
      const path=String(input);paths.push(path);
      if(path.endsWith("/session"))return json({roles:["team_admin"],capabilities:["team_directory"],team_id:"platform"});
      return json({data:[record],total:1});
    });
    show();await screen.findByText("Alice");
    expect(screen.queryByRole("button",{name:"Create User"})).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button",{name:"Actions for user u-1"}));
    expect(screen.getByRole("menuitem",{name:"Inspect"})).toHaveAttribute("href","/ui/users/u-1");
    expect(screen.queryByRole("menuitem",{name:"Edit"})).not.toBeInTheDocument();
    expect(screen.getByText("Directory scoped to team platform")).toBeInTheDocument();
    expect(paths.some(path=>/keys\?|financials|budgets/.test(path))).toBe(false);
  });

  it("does not let an old identity mutation close a new session's form",async()=>{
    let actor="first";
    let resolveSave!:(value:Response)=>void;
    vi.spyOn(globalThis,"fetch").mockImplementation(async(input,options)=>{
      if(String(input).endsWith("/session"))return json({...session,user_id:actor,credential_id:actor});
      if(options?.method==="POST")return new Promise(resolve=>{resolveSave=resolve;});
      return json({data:[{...record,name:actor}],total:1});
    });
    function Switch(){const{restoreSession}=useAuth();return <button onClick={()=>{actor="second";void restoreSession();}}>Switch verified session</button>;}
    sessionStorage.setItem("ai-gateway.admin-token","fixture");
    render(<MemoryRouter><AuthProvider><Switch/><Gate><IdentityDirectoryPage kind="users"/></Gate></AuthProvider></MemoryRouter>);
    await screen.findByText("first");
    await userEvent.click(screen.getByRole("button",{name:"Create User"}));
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button",{name:"Create user"}));
    await waitFor(()=>expect(resolveSave).toBeTypeOf("function"));
    await userEvent.click(screen.getByRole("button",{name:"Switch verified session"}));
    await screen.findByText("second");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button",{name:"Create User"}));
    resolveSave(json(record,201));
    await waitFor(()=>expect(screen.getByRole("dialog",{name:"Create user"})).toBeInTheDocument());
    expect(screen.queryByText(/User .* created\./)).not.toBeInTheDocument();
    expect(screen.getByText("second")).toBeInTheDocument();
  });
});
