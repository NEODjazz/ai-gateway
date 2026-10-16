import { useEffect, type ReactNode } from "react";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider, useAuth } from "../../auth/AuthContext";
import { IdentityPolicyPanel } from "./IdentityPolicyPanel";

const initial = { scope_type:"team",scope_id:"platform",model_access:"inherit",allowed_models:[] as string[],rpm:0,tpm:0 };
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value),{status,headers:{"Content-Type":"application/json"}});
function Gate({children}:{children:ReactNode}) {const {session,restoreSession}=useAuth();useEffect(()=>{if(!session)void restoreSession();},[session,restoreSession]);return session?children:null;}
function tree(id="platform",kind:"team"|"user"="team") {return <MemoryRouter><AuthProvider><Gate><IdentityPolicyPanel kind={kind} id={id}/></Gate></AuthProvider></MemoryRouter>;}
type Request={path:string;method:string;body?:Record<string,unknown>};
function fixture(handler?:(r:Request)=>Response|Promise<Response>|undefined,scoped=false) {
  sessionStorage.setItem("ai-gateway.admin-token","fixture");
  const requests:Request[]=[];
  const settings={policy:{...initial},configured:false,durable:true};
  vi.spyOn(globalThis,"fetch").mockImplementation(async(input,options)=>{
    const path=String(input),method=options?.method||"GET",request={path,method,body:options?.body?JSON.parse(String(options.body)):undefined};requests.push(request);
    if(path==="/admin/v1/session")return json({user_id:"operator",team_id:scoped?"platform":undefined,roles:[scoped?"team_admin":"admin"],capabilities:scoped?["team_directory"]:["admin"]});
    const custom=handler?.(request);if(custom)return custom;
    if(path==="/admin/v1/model-catalog")return json({models:[{model:"qwen3.8:27b"},{model:"llama3.2:latest"}]});
    if(path.startsWith("/admin/v1/identity-policies/")){
      const parts=path.split("/");
      if(method==="PUT"){Object.assign(settings.policy,request.body);settings.configured=true;}
      if(method==="DELETE"){settings.policy={...initial};settings.configured=false;}
      return json({...settings,policy:{...settings.policy,scope_type:parts[4],scope_id:parts[5]}});
    }
    return json({});
  });return {requests,settings};
}
describe("IdentityPolicyPanel",()=>{
  afterEach(()=>{vi.restoreAllMocks();sessionStorage.clear();});
  it("shows actual defaults and saves selected models with aggregate quotas pinned to the identity",async()=>{
    const {requests}=fixture();render(tree());
    await screen.findByText("No additional restriction");
    expect(screen.getAllByText("No additional limit")).toHaveLength(2);
    await userEvent.click(screen.getByRole("button",{name:"Edit models and limits"}));
    const dialog=screen.getByRole("dialog");
    await userEvent.selectOptions(within(dialog).getByLabelText("Model access"),"allowlist");
    await userEvent.click(within(dialog).getByRole("combobox",{name:"Allowed models"}));
    await userEvent.click(await screen.findByRole("option",{name:"qwen3.8:27b"}));
    await userEvent.clear(within(dialog).getByLabelText("Aggregate RPM"));await userEvent.type(within(dialog).getByLabelText("Aggregate RPM"),"30");
    await userEvent.clear(within(dialog).getByLabelText("Aggregate TPM"));await userEvent.type(within(dialog).getByLabelText("Aggregate TPM"),"90000");
    await userEvent.click(within(dialog).getByRole("button",{name:"Save models and limits"}));
    await waitFor(()=>expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(await screen.findByText("90,000")).toBeInTheDocument();
    expect(requests.find(r=>r.method==="PUT")).toEqual({path:"/admin/v1/identity-policies/team/platform",method:"PUT",body:{model_access:"allowlist",allowed_models:["qwen3.8:27b"],rpm:30,tpm:90000}});
  });
  it("does not turn an empty allowlist into unrestricted access and supports explicit deny",async()=>{
    const {requests}=fixture();render(tree("platform","user"));await screen.findByText("No additional restriction");
    await userEvent.click(screen.getByRole("button",{name:"Edit models and limits"}));const dialog=screen.getByRole("dialog");
    await userEvent.selectOptions(within(dialog).getByLabelText("Model access"),"allowlist");
    await userEvent.click(within(dialog).getByRole("button",{name:"Save models and limits"}));
    await within(dialog).findByText("Select 1–100 models or patterns. A wildcard is supported only at the end.");expect(requests.some(r=>r.method==="PUT")).toBe(false);
    await userEvent.selectOptions(within(dialog).getByLabelText("Model access"),"deny");await userEvent.click(within(dialog).getByRole("button",{name:"Save models and limits"}));
    await waitFor(()=>expect(screen.queryByRole("dialog")).not.toBeInTheDocument());expect(await screen.findByText("All models blocked")).toBeInTheDocument();
    expect(requests.find(r=>r.method==="PUT")?.path).toBe("/admin/v1/identity-policies/user/platform");
  });
  it("preserves a failed draft and prevents dismissal and duplicate submission while saving",async()=>{
    let attempts=0,resolve!:(response:Response)=>void;
    fixture(({method})=>method==="PUT"?(++attempts===1?json({error:{message:"Save failed"}},503):new Promise<Response>(r=>{resolve=r;})):undefined);render(tree());await screen.findByText("No additional restriction");
    await userEvent.click(screen.getByRole("button",{name:"Edit models and limits"}));const dialog=screen.getByRole("dialog");await userEvent.selectOptions(within(dialog).getByLabelText("Model access"),"deny");
    await userEvent.click(within(dialog).getByRole("button",{name:"Save models and limits"}));await within(dialog).findByText("Save failed");expect(within(dialog).getByLabelText("Model access")).toHaveValue("deny");
    await userEvent.click(within(dialog).getByRole("button",{name:"Save models and limits"}));await userEvent.keyboard("{Escape}");expect(screen.getByRole("dialog")).toBeInTheDocument();expect(within(dialog).getByRole("button",{name:"Saving…"})).toBeDisabled();expect(attempts).toBe(2);
    resolve(json({}));await waitFor(()=>expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
  it("allows a matching team administrator to inspect but never edit or fetch user-wide policies",async()=>{
    const {requests}=fixture(undefined,true);const view=render(tree());await screen.findByText("No additional restriction");expect(screen.queryByRole("button",{name:"Edit models and limits"})).not.toBeInTheDocument();
    view.rerender(tree("alice","user"));await screen.findByText("User-wide inference policy is restricted to global administrators.");
    expect(requests.some(r=>r.path.includes("/identity-policies/user/"))).toBe(false);
  });
  it("rejects a settings response for a different identity",async()=>{
    fixture(({path})=>path.includes("/identity-policies/")?json({policy:{...initial,scope_id:"foreign",rpm:999},configured:true,durable:true}):undefined);render(tree());
    await screen.findByText("The directory returned invalid data.");expect(screen.queryByText("999")).not.toBeInTheDocument();expect(screen.queryByRole("button",{name:"Edit models and limits"})).not.toBeInTheDocument();
  });
  it("confirms a reset, preserves policy on failure and clears it after success",async()=>{
    let fail=true;const {settings}=fixture(({method})=>method==="DELETE"&&fail?json({error:{message:"Reset failed"}},503):undefined);Object.assign(settings.policy,{model_access:"deny",rpm:60});settings.configured=true;
    vi.spyOn(window,"confirm").mockReturnValue(true);render(tree());await screen.findByText("All models blocked");
    await userEvent.click(screen.getByRole("button",{name:"Reset identity policy"}));await screen.findByText("Reset failed");expect(screen.getByText("All models blocked")).toBeInTheDocument();
    fail=false;await userEvent.click(screen.getByRole("button",{name:"Reset identity policy"}));await screen.findByText("No additional restriction");expect(screen.getByRole("button",{name:"Reset identity policy"})).toBeDisabled();
  });
  it("does not let an old save close another identity's new form",async()=>{
    let resolve!:(response:Response)=>void;
    fixture(({method,path})=>method==="PUT"&&path.endsWith("/platform")?new Promise<Response>(r=>{resolve=r;}):undefined);const view=render(tree());await screen.findByText("No additional restriction");
    await userEvent.click(screen.getByRole("button",{name:"Edit models and limits"}));await userEvent.click(within(screen.getByRole("dialog")).getByRole("button",{name:"Save models and limits"}));
    view.rerender(tree("research"));await screen.findByText("No additional restriction");await userEvent.click(screen.getByRole("button",{name:"Edit models and limits"}));
    resolve(json({}));await waitFor(()=>expect(within(screen.getByRole("dialog")).getByText("team · research")).toBeInTheDocument());expect(screen.queryByText(/Identity policy saved/)).not.toBeInTheDocument();
  });
  it.each([503,200])("retains explicit model patterns when the catalog is unavailable or invalid (%i)",async(status)=>{
    const {settings,requests}=fixture(({path})=>path==="/admin/v1/model-catalog"?json(status===503?{error:{message:"Catalog unavailable"}}:{models:"invalid"},status):undefined);Object.assign(settings.policy,{model_access:"allowlist",allowed_models:["private-*"]});settings.configured=true;
    render(tree());await screen.findByText("private-*");await userEvent.click(screen.getByRole("button",{name:"Edit models and limits"}));const dialog=screen.getByRole("dialog");
    await within(dialog).findByText("Model catalog is unavailable. Existing selections and explicit model patterns remain editable.");await userEvent.click(within(dialog).getByRole("button",{name:"Save models and limits"}));await waitFor(()=>expect(screen.queryByRole("dialog")).not.toBeInTheDocument());expect(requests.find(r=>r.method==="PUT")?.body?.allowed_models).toEqual(["private-*"]);
  });
});
