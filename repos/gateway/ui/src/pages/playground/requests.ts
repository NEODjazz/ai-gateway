import { APIClient } from "../../api/client";

export type TextEndpoint = "chat" | "responses";
export type Message = { role: string; content: unknown; tool_calls?: unknown; tool_call_id?: string; reasoning?: unknown; reasoning_content?: unknown; refusal?: unknown; annotations?: unknown; audio?: unknown; function_call?: unknown; responseItems?: unknown[] };
export type GenerationSettings = {
  maxTokens: string;
  temperature: string;
  topP: string;
  responseFormat: "text" | "json_object" | "json_schema";
  schema: string;
  advanced: string;
};
export const defaultGenerationSettings: GenerationSettings = {
  maxTokens: "256", temperature: "", topP: "", responseFormat: "text", schema: "", advanced: ""
};
export const textEndpointPaths: Record<TextEndpoint, string> = { chat: "/v1/chat/completions", responses: "/v1/responses" };
const maximumJSONBytes = 64 * 1024;
const protectedParameters = new Set(["model", "provider", "messages", "input", "instructions", "stream", "stream_options", "previous_response_id", "conversation", "max_tokens", "max_completion_tokens", "max_output_tokens", "temperature", "top_p", "response_format", "text", "__proto__", "prototype", "constructor"]);

export function optionalNumber(value: string, label: string, min: number, max: number, integer = false): number | undefined {
  if (!value.trim()) return undefined;
  const number = Number(value);
  if (!Number.isFinite(number) || number < min || number > max || (integer && !Number.isSafeInteger(number))) {
    throw new Error(`${label} must be ${integer ? "an integer" : "a number"} between ${min} and ${max}.`);
  }
  return number;
}

export function jsonObject(value: string, label: string): Record<string, unknown> {
  if (new TextEncoder().encode(value).length > maximumJSONBytes) throw new Error(`${label} exceeds 64 KiB.`);
  let parsed: unknown;
  try { parsed = JSON.parse(value); } catch { throw new Error(`${label} must be valid JSON.`); }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error(`${label} must be a JSON object.`);
  return parsed as Record<string, unknown>;
}

function responseContent(content: unknown): unknown {
  if (!Array.isArray(content)) return content;
  return content.map((value) => {
    if (!value || typeof value !== "object") throw new Error("Invalid content part.");
    const part = value as Record<string, unknown>;
    if (part.type === "text") return { ...part, type: "input_text" };
    if (part.type === "image_url") {
      const image = part.image_url as { url?: unknown; detail?: unknown } | undefined;
      if (typeof image?.url !== "string") throw new Error("Invalid image URL part.");
      return { type: "input_image", image_url: image.url, ...(image.detail ? { detail: image.detail } : {}) };
    }
    if (part.type === "file") {
      const file = part.file as Record<string, unknown> | undefined;
      if (!file) throw new Error("Invalid file part."); return { type: "input_file", ...file };
    }
    return part;
  });
}

function responseHistory(history: Message[]): unknown[] {
  return history.flatMap((message): unknown[] => {
    if (message.responseItems) return message.responseItems;
    if (message.role === "tool") {
      if (!message.tool_call_id) throw new Error("A tool result requires tool_call_id.");
      return [{ type: "function_call_output", call_id: message.tool_call_id, output: typeof message.content === "string" ? message.content : JSON.stringify(message.content) }];
    }
    const items: unknown[] = [];
    if (message.content != null && message.content !== "") items.push({ role: message.role, content: responseContent(message.content) });
    if (Array.isArray(message.tool_calls)) for (const value of message.tool_calls) {
      const call = value as { id?: string; function?: { name?: string; arguments?: string } };
      if (!call.id || !call.function?.name || typeof call.function.arguments !== "string") throw new Error("Invalid tool call in conversation history.");
      items.push({ type: "function_call", call_id: call.id, name: call.function.name, arguments: call.function.arguments });
    }
    return items;
  });
}

export function buildTextRequest({ endpoint, model, input, instructions, history = [], toolOutputs = [], previousResponseID = "", streaming, settings }: {
  endpoint: TextEndpoint; model: string; input?: unknown; instructions: string; history?: Message[]; toolOutputs?: Message[];
  previousResponseID?: string; streaming: boolean; settings: GenerationSettings;
}): Record<string, unknown> {
  if (typeof input === "string" && new TextEncoder().encode(input).length > 1024 * 1024) throw new Error("Prompt exceeds the 1 MiB Playground limit.");
  if (new TextEncoder().encode(instructions).length > 64 * 1024) throw new Error("Instructions exceed the 64 KiB Playground limit.");
  if (!model.trim()) throw new Error("Select a model before sending a request.");
  const limit = optionalNumber(settings.maxTokens, "Maximum output tokens", 1, Number.MAX_SAFE_INTEGER, true);
  const temperature = optionalNumber(settings.temperature, "Temperature", 0, 2);
  const topP = optionalNumber(settings.topP, "Top P", 0, 1);
  const extras = settings.advanced.trim() ? jsonObject(settings.advanced, "Advanced parameters") : {};
  for (const name of Object.keys(extras)) {
    if (protectedParameters.has(name)) throw new Error(`Configure ${name} with its dedicated control; advanced parameters cannot override it.`);
  }
  const format: Record<string, unknown> = { type: settings.responseFormat };
  if (settings.responseFormat === "json_schema") {
    const schema = jsonObject(settings.schema, "Output JSON schema");
    format.name = "playground_output"; format.schema = schema; format.strict = true;
  }
  const body: Record<string, unknown> = { ...extras, model: model.trim(), stream: streaming,
    ...(temperature === undefined ? {} : { temperature }), ...(topP === undefined ? {} : { top_p: topP }) };
  if (endpoint === "chat") {
    body.messages = [...(instructions.trim() ? [{ role: "system", content: instructions.trim() }] : []), ...history, ...toolOutputs, ...(input === undefined ? [] : [{ role: "user", content: input }])];
    if (limit !== undefined) body.max_completion_tokens = limit;
    if (streaming) body.stream_options = { include_usage: true };
    if (settings.responseFormat !== "text") {
      body.response_format = settings.responseFormat === "json_schema" ? { type: "json_schema", json_schema: { name: format.name, schema: format.schema, strict: true } } : format;
    }
  } else {
    const current = Array.isArray(input) ? [{ role: "user", content: responseContent(input) }] : input;
    body.input = input === undefined || toolOutputs.length
      ? [...(previousResponseID ? [] : responseHistory(history)), ...responseHistory(toolOutputs), ...(input === undefined ? [] : [{ role: "user", content: responseContent(input) }])]
      : previousResponseID || !history.length ? current : [...responseHistory(history), { role: "user", content: responseContent(input) }];
    if (instructions.trim()) body.instructions = instructions.trim();
    if (previousResponseID) body.previous_response_id = previousResponseID;
    if (limit !== undefined) body.max_output_tokens = limit;
    if (settings.responseFormat !== "text") body.text = { format };
  }
  if (new TextEncoder().encode(JSON.stringify(body)).length > 24 * 1024 * 1024) throw new Error("Request exceeds 24 MiB. Clear history or remove attachments before sending.");
  return body;
}

export type KeySource = "session" | "custom";
export type PlaygroundConnection = { client: APIClient; path: (path: string) => string; baseURL: string; source: KeySource };

export function normalizedBaseURL(value: string): string {
  if (!value.trim()) return "";
  let url: URL;
  try { url = new URL(value.trim()); } catch { throw new Error("Gateway base URL must be an absolute HTTP or HTTPS URL."); }
  if (!["http:", "https:"].includes(url.protocol) || url.username || url.password || url.search || url.hash) {
    throw new Error("Gateway base URL must use HTTP or HTTPS without credentials, query parameters or a fragment.");
  }
  return url.toString().replace(/\/+$/, "");
}

export function gatewayPath(baseURL: string, path: string): string {
  if (!path.startsWith("/") || path.startsWith("//")) throw new Error("Gateway endpoint must be an absolute path.");
  // A pasted SDK base URL may already include /v1.
  return baseURL ? `${baseURL.replace(/\/v1$/, "")}${path}` : path;
}

export function playgroundConnection(sessionClient: APIClient, source: KeySource, key: string, url: string): PlaygroundConnection {
  const baseURL = normalizedBaseURL(url);
  if (source === "session" && baseURL) throw new Error("Use an explicit test API key for a custom gateway URL. The console credential is never forwarded.");
  const normalizedKey = key.trim().replace(/^Bearer\s+/i, "");
  if (source === "custom" && !normalizedKey) throw new Error("Enter a test API key.");
  return { client: source === "session" ? sessionClient : new APIClient(() => normalizedKey, { credentials: "omit", sessionEvents: false }),
    path: (path) => gatewayPath(baseURL, path), baseURL, source };
}

function shellQuote(value: string) { return `'${value.replace(/'/g, `'"'"'`)}'`; }

export type CodeCheck = { path: string; body: unknown };
export function requestCode(language: "curl" | "python" | "javascript", path: string, body: unknown, baseURL = "", additionalHeaders: Record<string, string> = {}, binaryOutput = false, checks: CodeCheck[] = []): string {
  if (checks.length) {
    if (checks.length > 4 || checks.some((check) => check.path !== "/guardrails/apply_guardrail")) throw new Error("Unsupported code export preflight.");
    const preflight = checks.map((check) => {
      const example = requestCode(language, check.path, check.body, baseURL);
      if (language === "curl") return example.replace(/^curl /, "curl --fail-with-body ") + " | python3 -c 'import json,sys; sys.exit(0 if json.load(sys.stdin).get(\"allowed\") is True else 1)'";
      if (language === "python") return example.replace("    print(response.read().decode())", '    decision = json.load(response)\n    if decision.get("allowed") is not True: raise RuntimeError("Prompt policy did not allow generation")');
      return "{\n" + example.replace("console.log(await response.text());", 'if ((await response.json())?.allowed !== true) throw new Error("Prompt policy did not allow generation");') + "\n}";
    }).join("\n\n");
    return (language === "curl" ? "#!/usr/bin/env bash\nset -euo pipefail\n\n" : "") + preflight + "\n\n" + requestCode(language, path, body, baseURL, additionalHeaders, binaryOutput);
  }
  const url = gatewayPath(baseURL || window.location.origin, path);
  for (const [name, value] of Object.entries(additionalHeaders)) if (name !== "Idempotency-Key" || !/^[\x21-\x7e]{1,128}$/.test(value)) throw new Error("Unsupported code export header.");
  const headerJSON = JSON.stringify(additionalHeaders);
  const curlHeaders = Object.entries(additionalHeaders).map(([name, value]) => `  -H ${shellQuote(`${name}: ${value}`)} \\\n`).join("");
  const payload = JSON.stringify(body, null, 2);
  if (language === "curl") return `curl ${shellQuote(url)} \\\n  -H "Authorization: Bearer $GATEWAY_API_KEY" \\\n  -H 'Content-Type: application/json' \\\n${curlHeaders}  --data-raw ${shellQuote(payload)}${binaryOutput ? " --output ai-gateway-output.bin" : ""}`;
  if (language === "python") return `import json, os, urllib.request\n\nbody = json.loads(${JSON.stringify(payload)})\nrequest = urllib.request.Request(\n    ${JSON.stringify(url)},\n    data=json.dumps(body).encode(),\n    headers={"Authorization": "Bearer " + os.environ["GATEWAY_API_KEY"], "Content-Type": "application/json", **json.loads(${JSON.stringify(headerJSON)})},\n    method="POST",\n)\nwith urllib.request.urlopen(request, timeout=120) as response:\n${binaryOutput ? '    with open("ai-gateway-output.bin", "wb") as output:\n        output.write(response.read())' : "    print(response.read().decode())"}`;
  return `const response = await fetch(${JSON.stringify(url)}, {\n  method: "POST",\n  headers: {\n    Authorization: "Bearer " + process.env.GATEWAY_API_KEY,\n    "Content-Type": "application/json",\n    ...${headerJSON},\n  },\n  body: JSON.stringify(${payload}),\n});\nif (!response.ok) throw new Error("Request failed: " + response.status);\n${binaryOutput ? 'const { writeFile } = await import("node:fs/promises");\nawait writeFile("ai-gateway-output.bin", Buffer.from(await response.arrayBuffer()));' : "console.log(await response.text());"}`;
}
