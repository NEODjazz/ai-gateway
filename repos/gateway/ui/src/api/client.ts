export class APIError extends Error {
  constructor(public status: number, public code: string, message: string) {
    super(message);
    this.name = "APIError";
  }
}

export type RequestOptions = Omit<RequestInit, "body"> & { body?: unknown; maximumResponseBytes?: number; encodeJSON?: (body: unknown) => string; parseJSON?: (text: string) => unknown };
export type BinaryResponse = { body: Blob; contentType: string };
export type SSEEvent = { event: string; data: string };
export type StreamResult<T> = { streamed: true } | { streamed: false; data: T };
export type ClientOptions = { credentials?: RequestCredentials; sessionEvents?: boolean };

function parseSSEBlock(block: string): SSEEvent | undefined {
  let event = "message";
  const data: string[] = [];
  for (const line of block.replace(/\r/g, "").split("\n")) {
    if (!line || line.startsWith(":")) continue;
    if (line.startsWith("event:")) event = line.slice(6).trim() || "message";
    else if (line.startsWith("data:")) data.push(line.slice(5).replace(/^ /, ""));
  }
  return data.length ? { event, data: data.join("\n") } : undefined;
}

export class APIClient {
  constructor(private readonly getToken: () => string, private readonly options: ClientOptions = {}) {}

  private headers(options: RequestOptions, accept: string): Headers {
    const headers = new Headers(options.headers);
    const token = this.getToken();
    if (token) headers.set("Authorization", `Bearer ${token}`);
    if (options.body !== undefined) headers.set("Content-Type", "application/json");
    headers.set("Accept", accept);
    return headers;
  }

  private async throwResponseError(response: Response, maximumResponseBytes?: number): Promise<never> {
    let code = "request_failed";
    let message = `Request failed with status ${response.status}`;
    try {
      const payload = await this.json<{ error?: { code?: string; message?: string } }>(response, maximumResponseBytes === undefined ? undefined : Math.min(maximumResponseBytes, 64 * 1024));
      code = payload.error?.code || code;
      message = payload.error?.message || message;
    } catch {
      // Keep the bounded generic error; upstream response bodies are not exposed.
    }
    if (this.options.sessionEvents !== false && response.status === 409 && code === "revision_conflict") {
      window.dispatchEvent(new CustomEvent("control-plane-conflict"));
    }
    if (this.options.sessionEvents !== false && response.status === 401) {
      window.dispatchEvent(new CustomEvent("control-plane-session-expired"));
    }
    throw new APIError(response.status, code, message);
  }

  private async limitedBytes(response: Response, limit: number): Promise<Uint8Array<ArrayBuffer>[]> {
    const declared = Number(response.headers.get("Content-Length"));
    if (declared > limit) { await response.body?.cancel().catch(() => undefined); throw new APIError(response.status, "response_too_large", "Response exceeds the Playground size limit"); }
    if (!response.body) return [];
    const reader = response.body.getReader(), chunks: Uint8Array<ArrayBuffer>[] = [];
    let size = 0, complete = false;
    try {
      while (true) {
        const { value, done } = await reader.read(); if (done) break;
        size += value.byteLength;
        if (size > limit) throw new APIError(response.status, "response_too_large", "Response exceeds the Playground size limit");
        chunks.push(value.slice());
      }
      complete = true; return chunks;
    } finally { if (!complete) await reader.cancel().catch(() => undefined); reader.releaseLock(); }
  }

  private async json<T>(response: Response, limit?: number, parse?: (text: string) => unknown): Promise<T> {
    if (limit === undefined && !parse) return response.json() as Promise<T>;
    return (parse ?? JSON.parse)(await this.text(response, limit)) as T;
  }

  private async text(response: Response, limit?: number): Promise<string> {
    if (limit === undefined) return response.text();
    const decoder = new TextDecoder(); let text = "";
    for (const chunk of await this.limitedBytes(response, limit)) text += decoder.decode(chunk, { stream: true });
    return text + decoder.decode();
  }

  async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    return this.jsonRequest<T>(path, options, options.body === undefined ? undefined : (options.encodeJSON ?? JSON.stringify)(options.body));
  }

  // Validate pre-encoded JSON without replacing its original numeric literals.
  async requestJSONText(path: string, body: string, options: Omit<RequestOptions, "body"> = {}): Promise<string | undefined> {
    if (new TextEncoder().encode(body).length > 1024 * 1024) throw new Error("JSON request exceeds the 1 MiB limit.");
    try { JSON.parse(body); } catch { throw new Error("JSON request must contain valid JSON."); }
    return this.jsonRequest<string | undefined>(path, { ...options, body }, body, async (response, limit) => {
      const text = await this.text(response, limit);
      JSON.parse(text);
      return text;
    });
  }

  private async jsonRequest<T>(path: string, { maximumResponseBytes, encodeJSON: _encodeJSON, parseJSON, ...options }: RequestOptions, body: string | undefined, readResponse: ((response: Response, limit?: number) => Promise<T>) | undefined = undefined): Promise<T> {
    const response = await fetch(path, {
      credentials: this.options.credentials,
      ...options,
      headers: this.headers(options, "application/json"),
      body
    });
    if (!response.ok) return this.throwResponseError(response, maximumResponseBytes);
    if (response.status === 204) return undefined as T;
    return readResponse ? readResponse(response, maximumResponseBytes) : this.json<T>(response, maximumResponseBytes, parseJSON);
  }

  async requestForm<T>(path: string, body: FormData, options: Omit<RequestInit, "body"> = {}): Promise<T> {
    const response = await fetch(path, {
      credentials: this.options.credentials,
      ...options,
      headers: this.headers(options, "application/json"),
      body
    });
    if (!response.ok) return this.throwResponseError(response);
    if (response.status === 204) return undefined as T;
    return response.json() as Promise<T>;
  }

  async download(path: string, options: Omit<RequestInit, "body"> = {}): Promise<BinaryResponse> {
    const response = await fetch(path, {
      credentials: this.options.credentials,
      ...options,
      headers: this.headers(options, "application/octet-stream")
    });
    if (!response.ok) return this.throwResponseError(response);
    return { body: await response.blob(), contentType: response.headers.get("Content-Type") || "application/octet-stream" };
  }

  async requestBinary(path: string, { maximumResponseBytes, encodeJSON, parseJSON: _parseJSON, ...options }: RequestOptions): Promise<BinaryResponse> {
    const response = await fetch(path, {
      credentials: this.options.credentials,
      ...options,
      headers: this.headers(options, "application/octet-stream"),
      body: options.body === undefined ? undefined : (encodeJSON ?? JSON.stringify)(options.body)
    });
    if (!response.ok) return this.throwResponseError(response, maximumResponseBytes);
    const contentType = response.headers.get("Content-Type") || "application/octet-stream";
    const body = maximumResponseBytes === undefined ? await response.blob() : new Blob(await this.limitedBytes(response, maximumResponseBytes), { type: contentType });
    return { body, contentType };
  }

  async stream<T = never>(path: string, { maximumResponseBytes, encodeJSON, parseJSON, ...options }: RequestOptions, onEvent: (event: SSEEvent) => void, acceptJSONFallback = false): Promise<StreamResult<T>> {
    const response = await fetch(path, {
      credentials: this.options.credentials,
      ...options,
      headers: this.headers(options, "text/event-stream"),
      body: options.body === undefined ? undefined : (encodeJSON ?? JSON.stringify)(options.body)
    });
    if (!response.ok) return this.throwResponseError(response, maximumResponseBytes);
    const contentType = response.headers.get("Content-Type")?.toLowerCase() || "";
    if (!contentType.includes("text/event-stream")) {
      if (acceptJSONFallback && contentType.includes("application/json")) {
        return { streamed: false, data: await this.json<T>(response, maximumResponseBytes, parseJSON) };
      }
      throw new APIError(response.status, "invalid_stream", "Expected a text/event-stream response");
    }
    if (!response.body) throw new APIError(response.status, "invalid_stream", "Streaming response body is unavailable");

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    const dispatch = (block: string) => {
      const event = parseSSEBlock(block);
      if (event) onEvent(event);
    };
    let completed = false;
    let receivedBytes = 0;
    try {
      while (true) {
        const { done, value } = await reader.read();
        receivedBytes += value?.byteLength || 0;
        if (maximumResponseBytes !== undefined && receivedBytes > maximumResponseBytes) throw new APIError(response.status, "response_too_large", "Stream exceeds the Playground size limit");
        buffer += decoder.decode(value, { stream: !done });
        let boundary = buffer.replace(/\r\n/g, "\n").indexOf("\n\n");
        while (boundary >= 0) {
          const normalized = buffer.replace(/\r\n/g, "\n");
          dispatch(normalized.slice(0, boundary));
          buffer = normalized.slice(boundary + 2);
          boundary = buffer.indexOf("\n\n");
        }
        if (done) break;
      }
      if (buffer.trim()) dispatch(buffer);
      completed = true;
    } finally {
      if (!completed) await reader.cancel().catch(() => undefined);
      reader.releaseLock();
    }
    return { streamed: true };
  }
}
