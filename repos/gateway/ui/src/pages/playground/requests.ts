import { APIClient } from "../../api/client";

export type TextEndpoint = "chat" | "responses";
export type Message = { role: string; content: unknown; tool_calls?: unknown; tool_call_id?: string; reasoning?: unknown; reasoning_content?: unknown; refusal?: unknown; annotations?: unknown; audio?: unknown; function_call?: unknown };
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

export function buildTextRequest({ endpoint, model, input, instructions, history = [], previousResponseID = "", streaming, settings }: {
  endpoint: TextEndpoint; model: string; input: unknown; instructions: string; history?: Message[];
  previousResponseID?: string; streaming: boolean; settings: GenerationSettings;
}): Record<string, unknown> {
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
    body.messages = [...(instructions.trim() ? [{ role: "system", content: instructions.trim() }] : []), ...history, { role: "user", content: input }];
    if (limit !== undefined) body.max_completion_tokens = limit;
    if (streaming) body.stream_options = { include_usage: true };
    if (settings.responseFormat !== "text") {
      body.response_format = settings.responseFormat === "json_schema" ? { type: "json_schema", json_schema: { name: format.name, schema: format.schema, strict: true } } : format;
    }
  } else {
    body.input = previousResponseID ? input : history.length ? [...history, { role: "user", content: input }] : input;
    if (instructions.trim()) body.instructions = instructions.trim();
    if (previousResponseID) body.previous_response_id = previousResponseID;
    if (limit !== undefined) body.max_output_tokens = limit;
    if (settings.responseFormat !== "text") body.text = { format };
  }
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

export function requestCode(language: "curl" | "python" | "javascript", path: string, body: unknown, baseURL = ""): string {
  const url = gatewayPath(baseURL || window.location.origin, path);
  const payload = JSON.stringify(body, null, 2);
  if (language === "curl") return `curl ${shellQuote(url)} \\\n  -H "Authorization: Bearer $GATEWAY_API_KEY" \\\n  -H 'Content-Type: application/json' \\\n  --data-raw ${shellQuote(payload)}`;
  if (language === "python") return `import json, os, urllib.request\n\nbody = json.loads(${JSON.stringify(payload)})\nrequest = urllib.request.Request(\n    ${JSON.stringify(url)},\n    data=json.dumps(body).encode(),\n    headers={"Authorization": "Bearer " + os.environ["GATEWAY_API_KEY"], "Content-Type": "application/json"},\n    method="POST",\n)\nwith urllib.request.urlopen(request, timeout=120) as response:\n    print(response.read().decode())`;
  return `const response = await fetch(${JSON.stringify(url)}, {\n  method: "POST",\n  headers: {\n    Authorization: "Bearer " + process.env.GATEWAY_API_KEY,\n    "Content-Type": "application/json",\n  },\n  body: JSON.stringify(${payload}),\n});\nif (!response.ok) throw new Error("Request failed: " + response.status);\nconsole.log(await response.text());`;
}
