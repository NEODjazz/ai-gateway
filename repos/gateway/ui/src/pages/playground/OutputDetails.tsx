import { useState } from "react";
import { GatewayButton } from "../../components/GatewayButton";
import { safeMediaURL } from "./endpointRequests";

type Details = { tools: unknown[]; citations: { url: string; title: string }[]; images: string[] };
export function outputDetails(payload: unknown): Details {
  const details: Details = { tools: [], citations: [], images: [] };
  const seen = new Set<string>();
  let visited = 0;
  function visit(value: unknown, depth: number) {
    if (!value || typeof value !== "object" || depth > 8 || ++visited > 512) return;
    if (Array.isArray(value)) { for (const item of value) visit(item, depth + 1); return; }
    const item = value as Record<string, unknown>;
    if (["reasoning", "thought", "thinking", "redacted_thinking"].includes(String(item.type))) return;
    if (["custom_tool_call", "custom_tool_call_output", "function_call", "tool_use", "tool_result", "function_result", "mcp_call", "mcp_approval_request", "code_interpreter_call", "file_search_call"].includes(String(item.type))) details.tools.push(item);
    if (Array.isArray(item.tool_calls)) details.tools.push(...item.tool_calls);
    if (item.type === "url_citation" || item.type === "web_search_result_location") {
      const source = item.url_citation && typeof item.url_citation === "object" ? item.url_citation as Record<string, unknown> : item;
      const url = safeMediaURL(source.url);
      if (url && /^https?:/.test(url) && !seen.has(url)) { seen.add(url); details.citations.push({ url, title: typeof source.title === "string" ? source.title : url }); }
    }
    if (["image", "image_url", "output_image"].includes(String(item.type))) {
      const image = item.image_url && typeof item.image_url === "object" ? (item.image_url as Record<string, unknown>).url : item.url || item.image_url || (typeof item.data === "string" ? `data:${String(item.mime_type || "image/png")};base64,${item.data}` : undefined);
      const url = safeMediaURL(image); if (url && !seen.has(url)) { seen.add(url); details.images.push(url); }
    }
    // Inspect only protocol output fields. Do not recurse into tool arguments or reasoning.
    for (const key of ["choices", "message", "output", "outputs", "steps", "parts", "content", "annotations", "citations"]) visit(item[key], depth + 1);
  }
  visit(payload, 0);
  return details;
}

export function CopyOutput({ text, label = "Copy output" }: { text: string; label?: string }) {
  const [status, setStatus] = useState("");
  return <div className="playground-actions"><GatewayButton view="flat" disabled={!text} aria-label={label} onClick={async () => {
    try { await navigator.clipboard.writeText(text); setStatus("Copied"); } catch { setStatus("Could not copy output. Select the text to copy it."); }
  }}>Copy output</GatewayButton>{status && <span role="status">{status}</span>}</div>;
}

export function OutputDetails({ payload }: { payload: unknown }) {
  const details = outputDetails(payload);
  return <>
    {details.tools.length > 0 && <details open><summary>Tool calls and results</summary><pre>{JSON.stringify(details.tools, null, 2).slice(0, 65536)}</pre></details>}
    {details.citations.length > 0 && <div className="playground-citations"><strong>Sources</strong><ul>{details.citations.map((source) => <li key={source.url}><a href={source.url} target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer">{source.title}</a></li>)}</ul></div>}
    {details.images.map((url, index) => <figure key={url}><img className="playground-generated-image" src={url} alt={`Response image ${index + 1}`} referrerPolicy="no-referrer" /><a href={url} target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer">Open image</a></figure>)}
  </>;
}
