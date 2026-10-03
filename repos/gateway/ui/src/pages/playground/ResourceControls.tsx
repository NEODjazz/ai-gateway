import { useEffect, useRef, useState } from "react";
import { Checkbox } from "@gravity-ui/uikit";
import { GatewayButton } from "../../components/GatewayButton";
import { GravityThemeScope } from "../../components/GravityThemeScope";
import { SelectControl, TextControl } from "./Controls";
import type { PlaygroundConnection, TextEndpoint } from "./requests";
import { emptyResources, parseResourceCatalog, validateMCPSelection, validFunctionName, validResourceID, type MCPTool, type Resource, type ResourceCatalog, type ResourceSelection } from "./resources";

type OwnedPage = { items: Resource[]; after: string; more: boolean; loaded: boolean };
type ResourceKind = "vectors" | "containers" | "files";
const paths: Record<ResourceKind, string> = { vectors: "/v1/vector_stores", containers: "/v1/containers", files: "/v1/files" };
const labels = { vectors: "vector stores", containers: "containers", files: "files" };
const emptyPage = (): OwnedPage => ({ items: [], after: "", more: false, loaded: false });
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);

export function ResourceControls({ connection, model, endpoint, value, onUpdate, disabled }: {
  connection: PlaygroundConnection; model: string; endpoint: TextEndpoint; value: ResourceSelection; onUpdate: (value: ResourceSelection) => void; disabled: boolean;
}) {
  const [catalog, setCatalog] = useState<ResourceCatalog>(), [error, setError] = useState(""), [busy, setBusy] = useState(false);
  const [toolset, setToolset] = useState("all"), [server, setServer] = useState("");
  const [tools, setTools] = useState<MCPTool[]>([]), [cursor, setCursor] = useState(""), [loadedTools, setLoadedTools] = useState(false);
  const [owned, setOwned] = useState<Record<ResourceKind, OwnedPage>>({ vectors: emptyPage(), containers: emptyPage(), files: emptyPage() });
  const generation = useRef(0), abort = useRef<AbortController | undefined>(undefined), update = useRef(onUpdate);
  const scope = useRef({ connection, model, endpoint });
  update.current = onUpdate;
  useEffect(() => {
    generation.current++; abort.current?.abort(); setBusy(false); setCatalog(undefined); setError(""); setToolset("all"); setServer(""); setTools([]); setCursor(""); setLoadedTools(false);
    setOwned({ vectors: emptyPage(), containers: emptyPage(), files: emptyPage() });
    if (scope.current.connection !== connection || scope.current.model !== model || scope.current.endpoint !== endpoint) update.current({ ...emptyResources });
    scope.current = { connection, model, endpoint };
    return () => { generation.current++; abort.current?.abort(); };
  }, [connection, model, endpoint]);
  const blocked = disabled || busy;
  function patch(patch: Partial<ResourceSelection>) { onUpdate({ ...value, ...patch }); }
  function toggle(key: "policies" | "tags" | "vectors" | "files", item: string) {
    patch({ [key]: value[key].includes(item) ? value[key].filter((id) => id !== item) : [...value[key], item] });
  }
  async function load(task: (signal: AbortSignal, current: () => boolean) => Promise<void>) {
    if (blocked) return;
    const controller = new AbortController(), current = ++generation.current; abort.current?.abort(); abort.current = controller; setBusy(true); setError("");
    try { await task(controller.signal, () => current === generation.current && !controller.signal.aborted); }
    catch (cause) { if (current === generation.current) setError(cause instanceof Error ? cause.message : "Could not load resources."); }
    finally { if (current === generation.current) { setBusy(false); abort.current = undefined; } }
  }
  function loadCatalog() { void load(async (signal, current) => {
    const result = parseResourceCatalog(await connection.client.request<unknown>(connection.path(`/v1/playground/catalog${model ? `?model=${encodeURIComponent(model)}` : ""}`), { signal, maximumResponseBytes: 1024 * 1024 }));
    if (current()) setCatalog(result);
  }); }
  function loadOwned(kind: ResourceKind, more = false) { void load(async (signal, current) => {
    const page = owned[kind];
    const result = await connection.client.request<{ data: Record<string, unknown>[]; has_more: boolean; last_id?: string }>(connection.path(`${paths[kind]}?limit=100${more ? `&after=${encodeURIComponent(page.after)}` : ""}`), { signal, maximumResponseBytes: 8 * 1024 * 1024 });
    if (typeof result?.has_more !== "boolean" || !Array.isArray(result?.data) || result.data.length > 100 || result.data.some((item) => !object(item) || typeof item.id !== "string" || !validResourceID(item.id))) throw new Error(`Gateway returned invalid ${labels[kind]}.`);
    const after = result.last_id || "";
    if (result.has_more && (!validResourceID(after) || (more && after === page.after))) throw new Error("Resource pagination did not advance.");
    const items = result.data.map((item) => ({ id: String(item.id), name: String(item.name || item.filename || item.id) }));
    const merged = [...new Map([...(more ? page.items : []), ...items].map((item) => [item.id, item])).values()];
    if (merged.length > 500) throw new Error("Resource discovery reached its 500-item browser limit. Use a resource ID in advanced JSON.");
    if (current()) setOwned((previous) => ({ ...previous, [kind]: { items: merged, after, more: result.has_more === true, loaded: true } }));
  }); }
  function loadTools(more = false) { void load(async (signal, current) => {
    if (!validResourceID(server)) throw new Error("Enter a valid MCP server ID.");
    const result = await connection.client.request<{ tools: { name: string; description?: string; inputSchema: unknown }[]; nextCursor?: string }>(connection.path(`/v1/mcp/servers/${encodeURIComponent(server)}/tools${more ? `?cursor=${encodeURIComponent(cursor)}` : ""}`), { signal, maximumResponseBytes: 8 * 1024 * 1024 });
    if ((result?.nextCursor !== undefined && typeof result.nextCursor !== "string") || !Array.isArray(result?.tools) || result.tools.length > 256 || result.tools.some((tool) => typeof tool.name !== "string" || tool.name.length > 256 || (tool.description !== undefined && (typeof tool.description !== "string" || tool.description.length > 65536)) || !object(tool.inputSchema))) throw new Error("MCP returned invalid tool definitions.");
    const next = result.nextCursor || "";
    if (next.length > 2048 || next && more && next === cursor) throw new Error("MCP pagination did not advance.");
    const patterns = catalog?.mcp_toolsets.find((item) => item.id === toolset)?.tool_grants[server];
    const permitted = patterns ? result.tools.filter((tool) => patterns.some((pattern) => pattern === "*" || pattern === tool.name || pattern.endsWith("*") && tool.name.startsWith(pattern.slice(0, -1)))) : result.tools;
    const items = permitted.map((tool) => ({ serverID: server, name: tool.name, description: tool.description, inputSchema: tool.inputSchema as Record<string, unknown> }));
    const merged = [...new Map([...(more ? tools : []), ...items].map((tool) => [tool.name, tool])).values()];
    if (merged.length > 256) throw new Error("MCP discovery reached its 256-tool browser limit.");
    if (current()) { setTools(merged); setCursor(next); setLoadedTools(true); }
  }); }
  const selectedToolset = catalog?.mcp_toolsets.find((item) => item.id === toolset);
  const servers = catalog?.mcp_servers.filter((item) => !selectedToolset || selectedToolset.server_ids.includes(item.id)) || [];
  function ownedChoices(kind: "vectors" | "files") {
    return <><GatewayButton view="outlined" disabled={blocked} onClick={() => loadOwned(kind)}>Load {labels[kind]}</GatewayButton>
      <div className="playground-resource-list">{owned[kind].items.map((item) => <GravityThemeScope key={item.id}><Checkbox disabled={blocked} controlProps={{ "aria-label": `${kind === "vectors" ? "Vector store" : "Code file"} ${item.name}` }} checked={value[kind].includes(item.id)} onUpdate={() => toggle(kind, item.id)}>{item.name}</Checkbox></GravityThemeScope>)}</div>
      {owned[kind].loaded && !owned[kind].items.length && <p className="muted">No owned {labels[kind]} found.</p>}
      {owned[kind].more && <GatewayButton view="flat" disabled={blocked} onClick={() => loadOwned(kind, true)}>Load more {labels[kind]}</GatewayButton>}</>;
  }
  return <details className="playground-advanced"><summary>Tools, resources and policies</summary>
    <GatewayButton view="outlined" disabled={blocked} onClick={loadCatalog}>Load resource catalog</GatewayButton>
    {(value.tools.length > 0 || value.policies.length > 0 || value.tags.length > 0 || value.vectors.length > 0 || value.codeInterpreter) && <><p className="muted">Active selections: {value.tools.length} tools, {value.policies.join(", ") || "no additional policies"}, {value.tags.length} tags, {value.vectors.length} vector stores{value.codeInterpreter ? ", code interpreter" : ""}.</p><GatewayButton view="flat" disabled={blocked} onClick={() => onUpdate({ ...emptyResources })}>Clear resource selections</GatewayButton></>}
    <p className="muted">Discovery uses the active Playground credential. Loading MCP tools contacts the selected server through the gateway and is accounted as a discovery request.</p>
    {catalog?.truncated && <p role="status">Catalog limited to 256 entries per list. Use explicit IDs for other authorized resources.</p>}
    {!!catalog?.mcp_toolsets.length && <SelectControl label="MCP toolset" value={toolset} disabled={blocked} options={[{ value: "all", content: "All accessible servers" }, ...catalog.mcp_toolsets.map((item) => ({ value: item.id, content: item.name }))]} onUpdate={(id) => { setToolset(id); setServer(""); setTools([]); setCursor(""); setLoadedTools(false); patch({ tools: [] }); }} />}
    {servers.length ? <SelectControl label="Tool discovery server" value={server} disabled={blocked} options={servers.map((item) => ({ value: item.id, content: item.name }))} onUpdate={(id) => { setServer(id); setTools([]); setCursor(""); setLoadedTools(false); }} /> : <TextControl label="Tool discovery server" value={server} disabled={blocked} onUpdate={(id) => { setServer(id); setTools([]); setCursor(""); setLoadedTools(false); }} placeholder="Authorized MCP server ID" />}
    <GatewayButton view="outlined" disabled={blocked || !server} onClick={() => loadTools()}>Load MCP tools</GatewayButton>
    <div className="playground-resource-list">{tools.map((tool) => <GravityThemeScope key={tool.name}><Checkbox disabled={blocked || !validFunctionName(tool.name)} controlProps={{ "aria-label": `MCP function ${tool.name}` }} checked={value.tools.some((item) => item.serverID === tool.serverID && item.name === tool.name)} onUpdate={(checked) => {
      if (checked && value.tools.length >= 32) { setError("Select at most 32 MCP tools."); return; }
      const selected = checked ? [...value.tools, tool] : value.tools.filter((item) => item.serverID !== tool.serverID || item.name !== tool.name);
      try { validateMCPSelection(selected); patch({ tools: selected }); setError(""); } catch (cause) { setError(cause instanceof Error ? cause.message : "Tool selection exceeds the browser limit."); }
    }}>{tool.name}{!validFunctionName(tool.name) && " · incompatible function name"}</Checkbox></GravityThemeScope>)}</div>
    {loadedTools && !tools.length && <p className="muted">No accessible tools found.</p>}
    {cursor && <GatewayButton view="flat" disabled={blocked} onClick={() => loadTools(true)}>Load more MCP tools</GatewayButton>}
    {!!value.tools.length && <p className="muted">Selected: {value.tools.map((tool) => `${tool.serverID}/${tool.name}`).join(", ")}. The model receives function schemas; calls require separate approval. Function-name and MCP grants are checked independently by the gateway.</p>}
    {endpoint === "responses" && <><h3>Vector search</h3>{ownedChoices("vectors")}<GravityThemeScope><Checkbox controlProps={{ "aria-label": "Enable code interpreter" }} disabled={blocked} checked={value.codeInterpreter} onUpdate={(codeInterpreter) => patch({ codeInterpreter, ...(codeInterpreter ? {} : { files: [] }) })}>Code interpreter</Checkbox></GravityThemeScope>
      {value.codeInterpreter && <><GatewayButton view="outlined" disabled={blocked} onClick={() => loadOwned("containers")}>Load containers</GatewayButton><SelectControl label="Code interpreter container" value={value.container} disabled={blocked} options={[{ value: "auto", content: "Automatic container" }, ...owned.containers.items.map((item) => ({ value: item.id, content: item.name }))]} onUpdate={(container) => patch({ container, files: [] })} />{owned.containers.more && <GatewayButton view="flat" disabled={blocked} onClick={() => loadOwned("containers", true)}>Load more containers</GatewayButton>}{value.container === "auto" && ownedChoices("files")}</>}
    </>}
    {catalog?.tags.length ? <><h3>Request tags</h3><p className="muted">Selected tags are exported as request metadata. Credential tags and billing policy stay unchanged.</p><div className="playground-resource-list">{catalog.tags.map((tag) => <GravityThemeScope key={tag}><Checkbox disabled={blocked} controlProps={{ "aria-label": `Request tag ${tag}` }} checked={value.tags.includes(tag)} onUpdate={() => toggle("tags", tag)}>{tag}</Checkbox></GravityThemeScope>)}</div></> : null}
    <h3>Additional prompt checks</h3><p className="muted">Checks the current text prompt before generation. Instructions, history and attachments remain subject to mandatory gateway policies. A blocked or failed check stops generation.</p>
    {catalog?.policy_error && <p role="status" className="form-error">{catalog.policy_error}</p>}
    <div className="playground-resource-list">{catalog?.policies.map((policy) => <GravityThemeScope key={policy}><Checkbox disabled={blocked} controlProps={{ "aria-label": `Prompt policy ${policy}` }} checked={value.policies.includes(policy)} onUpdate={() => toggle("policies", policy)}>{policy}</Checkbox></GravityThemeScope>)}</div>
    {catalog && !catalog.policies.length && !catalog.policy_error && <p className="muted">No policies available for an additional check.</p>}
    {error && <p role="alert" className="form-error">Resource discovery: {error}</p>}
  </details>;
}
