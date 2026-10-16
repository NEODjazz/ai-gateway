import { useEffect, useRef, useState } from "react";
import { GatewayButton } from "../../components/GatewayButton";
import { SelectControl } from "./Controls";
import { validateAgentMCPTools, type AgentMCPTool } from "./agents";
import { parseResourceCatalog, type Resource } from "./resources";
import type { PlaygroundConnection } from "./requests";

export function AgentMCPControls({ connection, model, tools, disabled, onUpdate }: { connection: PlaygroundConnection; model: string; tools: AgentMCPTool[]; disabled: boolean; onUpdate: (tools: AgentMCPTool[]) => void }) {
  const [servers, setServers] = useState<Resource[]>([]), [server, setServer] = useState(""), [names, setNames] = useState<string[]>([]), [name, setName] = useState(""), [cursor, setCursor] = useState("");
  const [busy, setBusy] = useState(false), [error, setError] = useState(""), [notice, setNotice] = useState("");
  const epoch = useRef(0), abort = useRef<AbortController | undefined>(undefined), pages = useRef(0), cursors = useRef(new Set<string>());
  useEffect(() => {
    epoch.current++; abort.current?.abort(); setBusy(false); setServers([]); setServer(""); setNames([]); setName(""); setCursor(""); setError(""); setNotice(""); pages.current = 0; cursors.current.clear();
    return () => { epoch.current++; abort.current?.abort(); };
  }, [connection, model]);
  const blocked = disabled || busy;
  async function load(kind: "catalog" | "tools", more = false) {
    if (blocked || kind === "tools" && !server) return;
    const controller = new AbortController(), generation = ++epoch.current; abort.current?.abort(); abort.current = controller; setBusy(true); setError("");
    try {
      if (kind === "catalog") {
        const catalog = parseResourceCatalog(await connection.client.request(connection.path(`/v1/playground/catalog${model ? `?model=${encodeURIComponent(model)}` : ""}`), { signal: controller.signal, maximumResponseBytes: 1024 * 1024 }));
        if (generation !== epoch.current || controller.signal.aborted) return;
        setServers(catalog.mcp_servers); setNotice(catalog.truncated ? "Catalog is truncated; only listed servers can be selected here." : catalog.mcp_servers.length ? "" : "No MCP servers available to this credential.");
      } else {
        if (more && pages.current >= 8) throw new Error("MCP discovery is limited to eight pages per server.");
        const result = await connection.client.request<{ tools: { name: string }[]; nextCursor?: string }>(connection.path(`/v1/mcp/servers/${encodeURIComponent(server)}/tools${more ? `?cursor=${encodeURIComponent(cursor)}` : ""}`), { signal: controller.signal, maximumResponseBytes: 2 * 1024 * 1024 });
        if (generation !== epoch.current || controller.signal.aborted) return;
        if (!Array.isArray(result?.tools) || result.tools.length > 256 || result.tools.some((tool) => !tool || typeof tool.name !== "string" || !tool.name || new TextEncoder().encode(tool.name).length > 256) || result.nextCursor !== undefined && (typeof result.nextCursor !== "string" || result.nextCursor.length > 2048)) throw new Error("MCP returned invalid tool names or pagination.");
        const next = result.nextCursor || "";
        if (next && more && cursors.current.has(next)) throw new Error("MCP tool pagination did not advance.");
        if (!more) { pages.current = 0; cursors.current.clear(); }
        if (next) cursors.current.add(next); pages.current++;
        const combined = [...new Set([...(more ? names : []), ...result.tools.map((tool) => tool.name)])]; setNames(combined); setName(combined[0] || ""); setCursor(next); setNotice(combined.length ? "" : "No tools available to this credential on this server.");
      }
    } catch (cause) { if (generation === epoch.current && !controller.signal.aborted) setError(cause instanceof Error ? cause.message : "MCP discovery failed."); }
    finally { if (generation === epoch.current) { setBusy(false); abort.current = undefined; } }
  }
  function add() {
    if (blocked || !server || !names.includes(name)) return;
    const next = [...tools, { server_id: server, tool_name: name }];
    try { validateAgentMCPTools(next); onUpdate(next); setError(""); } catch (cause) { setError(cause instanceof Error ? cause.message : "Invalid MCP bindings."); }
  }
  return <section className="playground-agent-mcp-controls" aria-label="Agent MCP tools"><h3>MCP tools</h3><p className="muted">Save registry references only. The gateway discovers schemas and checks the execution credential on each run. Discovery can create billable resource operations.</p>
    <GatewayButton view="outlined" disabled={blocked} onClick={() => void load("catalog")}>Load agent MCP servers</GatewayButton>
    <SelectControl label="Agent MCP server" value={server} disabled={blocked} options={[{ value: "", content: "Choose an authorized server" }, ...servers.map((item) => ({ value: item.id, content: item.name }))]} onUpdate={(value) => { setServer(value); setNames([]); setName(""); setCursor(""); setError(""); setNotice(""); pages.current = 0; cursors.current.clear(); }} />
    <GatewayButton view="outlined" disabled={blocked || !server} onClick={() => void load("tools")}>Discover agent MCP tools</GatewayButton>
    <SelectControl label="Agent MCP tool" value={name} disabled={blocked || !names.length} options={[{ value: "", content: "Choose a discovered tool" }, ...names.map((value) => ({ value, content: value }))]} onUpdate={setName} />
    {cursor && <GatewayButton view="flat" disabled={blocked || pages.current >= 8} onClick={() => void load("tools", true)}>Load more agent MCP tools</GatewayButton>}
    <GatewayButton view="outlined" disabled={blocked || !name || tools.length >= 32 || tools.some((tool) => tool.server_id === server && tool.tool_name === name)} onClick={add}>Add agent MCP tool</GatewayButton>
    {!!tools.length && <ul>{tools.map((tool) => <li key={JSON.stringify(tool)}>{tool.server_id} / {tool.tool_name} <GatewayButton view="flat" aria-label={`Remove agent tool ${tool.server_id}/${tool.tool_name}`} disabled={blocked} onClick={() => onUpdate(tools.filter((item) => item.server_id !== tool.server_id || item.tool_name !== tool.tool_name))}>Remove</GatewayButton></li>)}</ul>}
    {notice && <p role="status">{notice}</p>}{busy && <p role="status">Discovering agent MCP tools…</p>}{error && <p role="alert" className="form-error">{error}</p>}
  </section>;
}
