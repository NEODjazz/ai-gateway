import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { APIClient } from "../../api/client";
import { AgentMCPControls } from "./AgentMCPControls";
import { agentBody, agentDraft, type AgentMCPTool } from "./agents";
import { playgroundConnection } from "./requests";
const connection = () => playgroundConnection(new APIClient(() => "test-key"), "session", "", "");
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const catalog = { mcp_servers: [{ id: "weather", name: "Weather" }], mcp_toolsets: [], tags: [], policies: [], agents: [], truncated: false };
function Harness() { const [tools, setTools] = useState<AgentMCPTool[]>([]); const [client] = useState(connection); return <AgentMCPControls connection={client} model="model" tools={tools} onUpdate={setTools} disabled={false} />; }
async function selectServer() { await userEvent.click(screen.getByRole("button", { name: "Load agent MCP servers" })); await waitFor(() => expect(screen.getByLabelText("Agent MCP server")).toBeEnabled()); await userEvent.click(screen.getByLabelText("Agent MCP server")); await userEvent.click(screen.getByRole("option", { name: "Weather" })); }

describe("saved agent MCP bindings", () => {
 it("discovers scoped servers/tools explicitly, selects and removes references", async () => {
  const mock = vi.spyOn(globalThis,"fetch").mockImplementation(async (url) => String(url).includes("catalog") ? json(catalog) : json({tools:[{name:"forecast",inputSchema:{type:"object"}}]}));
  render(<Harness />); expect(mock).not.toHaveBeenCalled(); await selectServer();
  await userEvent.click(screen.getByRole("button",{name:"Discover agent MCP tools"})); await waitFor(()=>expect(screen.getByRole("button",{name:"Add agent MCP tool"})).toBeEnabled());
  await userEvent.click(screen.getByRole("button",{name:"Add agent MCP tool"})); expect(screen.getByRole("button",{name:"Add agent MCP tool"})).toBeDisabled();
  await userEvent.click(screen.getByRole("button",{name:"Remove agent tool weather/forecast"})); expect(screen.getByRole("button",{name:"Add agent MCP tool"})).toBeEnabled();
  expect(String(mock.mock.calls[0][0])).toBe("/v1/playground/catalog?model=model"); expect(mock.mock.calls.filter(([url])=>String(url).includes("/tools"))).toHaveLength(1);
 });
 it("preserves saved bindings while a discovery endpoint fails",async()=>{
  vi.spyOn(globalThis,"fetch").mockResolvedValue(json({error:{message:"Discovery unavailable"}},503));
  const update=vi.fn(); render(<AgentMCPControls connection={connection()} model="model" tools={[{server_id:"weather",tool_name:"forecast"}]} onUpdate={update} disabled={false}/>);
  await userEvent.click(screen.getByRole("button",{name:"Load agent MCP servers"})); expect(await screen.findByRole("alert")).toHaveTextContent("Discovery unavailable"); expect(screen.getByRole("button",{name:"Remove agent tool weather/forecast"})).toBeEnabled(); expect(update).not.toHaveBeenCalled();
 });
 it("rejects repeated pagination and does not publish late old-credential discoveries",async()=>{
  const mock=vi.spyOn(globalThis,"fetch").mockImplementation(async(url)=>String(url).includes("catalog")?json(catalog):json({tools:[{name:"forecast"}],nextCursor:"same"}));
  const update=vi.fn(), view=render(<AgentMCPControls connection={connection()} model="model" tools={[]} onUpdate={update} disabled={false}/>); await selectServer();
  await userEvent.click(screen.getByRole("button",{name:"Discover agent MCP tools"})); await waitFor(()=>expect(screen.getByRole("button",{name:"Load more agent MCP tools"})).toBeEnabled()); await userEvent.click(screen.getByRole("button",{name:"Load more agent MCP tools"})); expect(await screen.findByRole("alert")).toHaveTextContent("did not advance");
  let resolve:(response:Response)=>void=()=>undefined; mock.mockImplementationOnce(()=>new Promise<Response>((done)=>{resolve=done;}));
  await userEvent.click(screen.getByRole("button",{name:"Load agent MCP servers"})); view.rerender(<AgentMCPControls connection={connection()} model="other" tools={[]} onUpdate={update} disabled={false}/>);
  await act(async()=>resolve(json(catalog))); await userEvent.click(screen.getByLabelText("Agent MCP server")); expect(screen.queryByRole("option",{name:"Weather"})).not.toBeInTheDocument(); expect(update).not.toHaveBeenCalled();
 });
 it("round-trips references without schema/credentials and rejects duplicates or excess tools",()=>{
  const binding={server_id:"weather",tool_name:"forecast"};
  const profile={id:"a",name:"A",model:"model",instructions_configured:false,tool_policy_id:"safe",allowed_tools:["forecast"],max_tool_calls:2,max_iterations:3,enabled:true,execution_supported:true,mcp_tools:[binding]};
  const draft=agentDraft(profile,""); const body=agentBody(draft); expect(body.mcp_tools).toEqual([binding]); draft.tools[0].tool_name="changed"; expect(profile.mcp_tools[0].tool_name).toBe("forecast");expect(body.mcp_tools[0].tool_name).toBe("forecast");
  expect(()=>agentBody({...draft,tools:[binding,binding]})).toThrow("32 distinct"); expect(()=>agentBody({...draft,tools:Array.from({length:33},(_,i)=>({...binding,tool_name:`tool${i}`}))})).toThrow("32 distinct");expect(()=>agentBody({...draft,tools:[{...binding,tool_name:"bad\0name"}]})).toThrow("32 distinct");
 });
});
