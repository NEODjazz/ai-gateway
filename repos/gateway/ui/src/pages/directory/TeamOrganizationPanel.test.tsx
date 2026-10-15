import { useEffect, useState, type ReactNode } from "react";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider, useAuth } from "../../auth/AuthContext";
import { IdentityForm } from "./IdentityForm";
import { OrganizationPicker, type OrganizationMetadata } from "./OrganizationPicker";
import { TeamOrganizationPanel } from "./TeamOrganizationPanel";

const json=(value:unknown,status=200)=>new Response(JSON.stringify(value),{status,headers:{"Content-Type":"application/json"}});
const owner:OrganizationMetadata={id:"owner",name:"Current tenant",status:"active"};
function Gate({children}:{children:ReactNode}){const {session,restoreSession}=useAuth();useEffect(()=>{if(!session)void restoreSession();},[session,restoreSession]);return session?children:null;}
function tree(id="platform",child?:ReactNode){return <MemoryRouter><AuthProvider><Gate>{child||<TeamOrganizationPanel id={id}/>}</Gate></AuthProvider></MemoryRouter>;}
type Request={path:string;method:string;body?:Record<string,unknown>};
function fixture(initial?:OrganizationMetadata,handler?:(r:Request)=>Response|Promise<Response>|undefined,scoped=false){
  sessionStorage.setItem("ai-gateway.admin-token","fixture");let current=initial;const requests:Request[]=[];
  const organizations=Array.from({length:620},(_,i)=>({id:`org-${i+1}`,name:i===619?"Late organization":`Org ${String(i+1).padStart(4,"0")}`,status:"active" as const}));
  vi.spyOn(globalThis,"fetch").mockImplementation(async(input,options)=>{
    const path=String(input),url=new URL(path,"http://example.test");const request={path,method:options?.method||"GET",body:options?.body?JSON.parse(String(options.body)):undefined};requests.push(request);
    if(url.pathname==="/admin/v1/session")return json({user_id:"operator",team_id:scoped?"platform":undefined,roles:[scoped?"team_admin":"admin"],capabilities:scoped?["team_directory"]:["admin","team_directory"]});
    const custom=handler?.(request);if(custom)return custom;
    if(url.pathname==="/admin/v1/organizations/page"){
      const offset=Number(url.searchParams.get("offset")),filtered=organizations.filter(row=>`${row.id} ${row.name}`.toLowerCase().includes((url.searchParams.get("search")||"").toLowerCase()));
      return json({data:filtered.slice(offset,offset+25),total:filtered.length,offset,limit:25});
    }
    if(url.pathname.endsWith("/organization")){
      if(request.method==="PUT")current=organizations.find(row=>row.id===request.body?.organization_id);
      if(request.method==="DELETE")current=undefined;
      return json({team_id:url.pathname.split("/")[4],organization:current||null});
    }
    return json({});
  });return {requests,organizations};
}
describe("Team organization ownership",()=>{
  afterEach(()=>{vi.restoreAllMocks();sessionStorage.clear();});
  it("searches beyond the first 500 organizations and saves the exact team assignment",async()=>{
    const {requests}=fixture();render(tree());await screen.findByText("No organization is assigned to this team.");
    await userEvent.click(screen.getByRole("button",{name:"Assign organization"}));const dialog=screen.getByRole("dialog",{name:"Assign team organization"});
    await within(dialog).findByText("1–25 of 620");await userEvent.type(within(dialog).getByRole("searchbox",{name:"Search organizations to assign"}),"late");
    await userEvent.selectOptions(within(dialog).getByLabelText("Team organization"),await within(dialog).findByRole("option",{name:"Late organization · org-620"}));
    await userEvent.click(within(dialog).getByRole("button",{name:"Assign organization"}));await waitFor(()=>expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(await screen.findByText("Late organization")).toBeInTheDocument();expect(screen.queryByRole("button",{name:"Assign organization"})).not.toBeInTheDocument();
    expect(requests.find(r=>r.method==="PUT")).toEqual({path:"/admin/v1/teams/platform/organization",method:"PUT",body:{organization_id:"org-620"}});
    expect(requests.some(r=>r.path.includes("limit=500")||r.path==="/admin/v1/organizations")).toBe(false);
  });
  it("retains an explicit selection across organization pages",async()=>{
    fixture();function Picker(){const [selected,setSelected]=useState<OrganizationMetadata>();return <OrganizationPicker selected={selected} onChange={setSelected}/>;}
    render(tree("platform",<Picker/>));await screen.findByRole("option",{name:"Org 0001 · org-1"});await userEvent.selectOptions(screen.getByLabelText("Team organization"),"org-1");
    await userEvent.click(screen.getByRole("button",{name:"Next organization choices"}));await screen.findByRole("option",{name:"Org 0026 · org-26"});expect(screen.getByLabelText("Team organization")).toHaveValue("org-1");expect(screen.getByText("26–50 of 620")).toBeInTheDocument();
  });
  it("preserves a conflict draft and blocks repeat submission or Escape while saving",async()=>{
    let attempts=0,resolve!:(response:Response)=>void;fixture(undefined,({method})=>method==="PUT"?(++attempts===1?json({error:{message:"Team already belongs to another organization"}},409):new Promise<Response>(r=>{resolve=r;})):undefined);
    render(tree());await screen.findByText("No organization is assigned to this team.");await userEvent.click(screen.getByRole("button",{name:"Assign organization"}));const dialog=screen.getByRole("dialog");
    await userEvent.selectOptions(within(dialog).getByLabelText("Team organization"),await screen.findByRole("option",{name:"Org 0001 · org-1"}));await userEvent.click(within(dialog).getByRole("button",{name:"Assign organization"}));
    await within(dialog).findByText("Team already belongs to another organization");expect(within(dialog).getByLabelText("Team organization")).toHaveValue("org-1");
    await userEvent.click(within(dialog).getByRole("button",{name:"Assign organization"}));await userEvent.keyboard("{Escape}");expect(screen.getByRole("dialog")).toBeInTheDocument();expect(within(dialog).getByRole("button",{name:"Saving…"})).toBeDisabled();expect(attempts).toBe(2);
    resolve(json({}));await waitFor(()=>expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
  it("requires explicit unassignment, surfaces errors and sends the expected previous owner",async()=>{
    let fail=true;const {requests}=fixture(owner,({method})=>method==="DELETE"&&fail?json({error:{message:"Ownership changed; refresh"}},404):undefined);vi.spyOn(window,"confirm").mockReturnValue(true);render(tree());await screen.findByText("Current tenant");
    expect(screen.queryByRole("button",{name:"Assign organization"})).not.toBeInTheDocument();await userEvent.click(screen.getByRole("button",{name:"Unassign organization"}));await screen.findByText("Ownership changed; refresh");expect(screen.getByText("Current tenant")).toBeInTheDocument();
    expect(requests.find(r=>r.method==="DELETE")?.body).toEqual({organization_id:"owner"});fail=false;await userEvent.click(screen.getByRole("button",{name:"Unassign organization"}));await screen.findByText("No organization is assigned to this team.");expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining("without rewriting credentials or principal bindings"));
  });
  it("shows only the matching team's ownership to a scoped administrator",async()=>{
    const {requests}=fixture(owner,undefined,true);render(tree());await screen.findByText("Current tenant");expect(screen.queryByRole("button",{name:"Unassign organization"})).not.toBeInTheDocument();expect(screen.queryByRole("button",{name:"Assign organization"})).not.toBeInTheDocument();
    expect(requests.filter(r=>r.path.includes("organizations/page"))).toHaveLength(0);
  });
  it.each([{team_id:"foreign",organization:owner},{team_id:"platform"}])("rejects an invalid owner response without showing an unassigned state",async(result)=>{
    fixture(undefined,({path})=>path.endsWith("/organization")?json(result):undefined);render(tree());await screen.findByText("The directory returned invalid data.");expect(screen.queryByText("Current tenant")).not.toBeInTheDocument();expect(screen.queryByText("No organization is assigned to this team.")).not.toBeInTheDocument();expect(screen.queryByRole("button",{name:"Assign organization"})).not.toBeInTheDocument();
  });
  it("ignores an old save completing after another team's form is opened",async()=>{
    let resolve!:(response:Response)=>void;fixture(undefined,({path,method})=>path.includes("/platform/")&&method==="PUT"?new Promise<Response>(r=>{resolve=r;}):undefined);const view=render(tree());await screen.findByText("No organization is assigned to this team.");await userEvent.click(screen.getByRole("button",{name:"Assign organization"}));let dialog=screen.getByRole("dialog");
    await userEvent.selectOptions(within(dialog).getByLabelText("Team organization"),await screen.findByRole("option",{name:"Org 0001 · org-1"}));await userEvent.click(within(dialog).getByRole("button",{name:"Assign organization"}));
    view.rerender(tree("research"));await screen.findByText("No organization is assigned to this team.");await userEvent.click(screen.getByRole("button",{name:"Assign organization"}));dialog=screen.getByRole("dialog");resolve(json({}));await waitFor(()=>expect(dialog).toBeInTheDocument());expect(screen.queryByText("Organization assignment saved.")).not.toBeInTheDocument();
  });
  it("submits the selected organization with new team creation and retains it after an atomic save failure",async()=>{
    fixture();const save=vi.fn().mockRejectedValueOnce(new Error("Organization no longer active")).mockResolvedValueOnce(undefined);render(tree("platform",<IdentityForm kind="teams" onClose={vi.fn()} onSave={save}/>));const dialog=await screen.findByRole("dialog",{name:"Create team"});
    await userEvent.type(within(dialog).getByLabelText("Name"),"New team");await userEvent.click(within(dialog).getByRole("checkbox",{name:"Assign to organization"}));await userEvent.type(within(dialog).getByRole("searchbox",{name:"Search organizations to assign"}),"late");
    await userEvent.selectOptions(within(dialog).getByLabelText("Team organization"),await screen.findByRole("option",{name:"Late organization · org-620"}));await userEvent.click(within(dialog).getByRole("button",{name:"Create team"}));await within(dialog).findByText("Organization no longer active");expect(within(dialog).getByLabelText("Team organization")).toHaveValue("org-620");expect(save.mock.calls[0][0]).toMatchObject({name:"New team",organization_id:"org-620",status:"active"});
  });
});
