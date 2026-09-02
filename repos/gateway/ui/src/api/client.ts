export class APIError extends Error {
  constructor(public status: number, public code: string, message: string) {
    super(message);
    this.name = "APIError";
  }
}

export type RequestOptions = Omit<RequestInit, "body"> & { body?: unknown };
export type SSEEvent = { event: string; data: string };
export type StreamResult<T> = { streamed: true } | { streamed: false; data: T };

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
  constructor(private readonly getToken: () => string) {}

  private headers(options: RequestOptions, accept: string): Headers {
    const headers = new Headers(options.headers);
    const token = this.getToken();
    if (token) headers.set("Authorization", `Bearer ${token}`);
    if (options.body !== undefined) headers.set("Content-Type", "application/json");
    headers.set("Accept", accept);
    return headers;
  }

  private async throwResponseError(response: Response): Promise<never> {
    let code = "request_failed";
    let message = `Request failed with status ${response.status}`;
    try {
      const payload = await response.json() as { error?: { code?: string; message?: string } };
      code = payload.error?.code || code;
      message = payload.error?.message || message;
    } catch {
      // Keep the bounded generic error; upstream response bodies are not exposed.
    }
    if (response.status === 409 && code === "revision_conflict") {
      window.dispatchEvent(new CustomEvent("control-plane-conflict"));
    }
    if (response.status === 401) {
      window.dispatchEvent(new CustomEvent("control-plane-session-expired"));
    }
    throw new APIError(response.status, code, message);
  }

  async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    const response = await fetch(path, {
      ...options,
      headers: this.headers(options, "application/json"),
      body: options.body === undefined ? undefined : JSON.stringify(options.body)
    });
    if (!response.ok) return this.throwResponseError(response);
    if (response.status === 204) return undefined as T;
    return response.json() as Promise<T>;
  }

  async stream<T = never>(path: string, options: RequestOptions, onEvent: (event: SSEEvent) => void, acceptJSONFallback = false): Promise<StreamResult<T>> {
    const response = await fetch(path, {
      ...options,
      headers: this.headers(options, "text/event-stream"),
      body: options.body === undefined ? undefined : JSON.stringify(options.body)
    });
    if (!response.ok) return this.throwResponseError(response);
    const contentType = response.headers.get("Content-Type")?.toLowerCase() || "";
    if (!contentType.includes("text/event-stream")) {
      if (acceptJSONFallback && contentType.includes("application/json")) {
        return { streamed: false, data: await response.json() as T };
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
    try {
      while (true) {
        const { done, value } = await reader.read();
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
