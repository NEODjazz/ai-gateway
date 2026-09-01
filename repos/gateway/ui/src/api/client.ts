export class APIError extends Error {
  constructor(public status: number, public code: string, message: string) {
    super(message);
    this.name = "APIError";
  }
}

export type RequestOptions = Omit<RequestInit, "body"> & { body?: unknown };

export class APIClient {
  constructor(private readonly getToken: () => string) {}

  async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    const headers = new Headers(options.headers);
    const token = this.getToken();
    if (token) headers.set("Authorization", `Bearer ${token}`);
    if (options.body !== undefined) headers.set("Content-Type", "application/json");
    headers.set("Accept", "application/json");
    const response = await fetch(path, {
      ...options,
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body)
    });
    if (!response.ok) {
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
    if (response.status === 204) return undefined as T;
    return response.json() as Promise<T>;
  }
}
