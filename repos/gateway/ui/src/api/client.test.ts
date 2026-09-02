import { APIClient, APIError } from "./client";

describe("APIClient", () => {
  it("sends bearer and JSON headers", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200, headers: { "Content-Type": "application/json" } }));
    await new APIClient(() => "admin-token").request("/admin/v1/test", { method: "POST", body: { value: 1 } });
    const request = fetchMock.mock.calls[0];
    const headers = new Headers(request[1]?.headers);
    expect(headers.get("Authorization")).toBe("Bearer admin-token");
    expect(headers.get("Content-Type")).toBe("application/json");
    expect(request[1]?.body).toBe('{"value":1}');
  });

  it("does not send authorization without a token", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    await new APIClient(() => "").request("/healthz");
    expect(new Headers(fetchMock.mock.calls[0][1]?.headers).has("Authorization")).toBe(false);
  });

  it("returns undefined for a 204 response", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(null, { status: 204 }));
    await expect(new APIClient(() => "x").request("/resource", { method: "DELETE" })).resolves.toBeUndefined();
  });

  it("maps bounded API errors", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ error: { code: "invalid_request", message: "Invalid input" } }), { status: 400 }));
    await expect(new APIClient(() => "x").request("/resource")).rejects.toEqual(expect.objectContaining<Partial<APIError>>({ status: 400, code: "invalid_request", message: "Invalid input" }));
  });

  it("emits a control-plane event on revision conflicts", async () => {
    const listener = vi.fn();
    window.addEventListener("control-plane-conflict", listener);
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ error: { code: "revision_conflict", message: "Retry" } }), { status: 409 }));
    await expect(new APIClient(() => "x").request("/resource")).rejects.toThrow("Retry");
    expect(listener).toHaveBeenCalledOnce();
  });

  it("emits a session-expired event on unauthorized responses", async () => {
    const listener = vi.fn();
    window.addEventListener("control-plane-session-expired", listener, { once: true });
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ error: { code: "unauthorized", message: "Expired" } }), { status: 401 }));
    await expect(new APIClient(() => "x").request("/resource")).rejects.toThrow("Expired");
    expect(listener).toHaveBeenCalledOnce();
  });

  it("parses chunked SSE events and sends the bearer without exposing it to callbacks", async () => {
    const encoder = new TextEncoder();
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(encoder.encode('event: response.output_text.delta\r\ndata: {"delta":"hel"}\r\n'));
        controller.enqueue(encoder.encode('\r\ndata: [DONE]\n\n'));
        controller.close();
      }
    });
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } }));
    const events: Array<{ event: string; data: string }> = [];
    await new APIClient(() => "stream-token").stream("/v1/responses", { method: "POST", body: { stream: true } }, (event) => events.push(event));
    expect(events).toEqual([
      { event: "response.output_text.delta", data: '{"delta":"hel"}' },
      { event: "message", data: "[DONE]" }
    ]);
    const headers = new Headers(fetchMock.mock.calls[0][1]?.headers);
    expect(headers.get("Authorization")).toBe("Bearer stream-token");
    expect(headers.get("Accept")).toBe("text/event-stream");
    expect(JSON.stringify(events)).not.toContain("stream-token");
  });

  it("maps a non-streaming HTTP error before reading SSE data", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ error: { code: "provider_failed", message: "No route" } }), { status: 502 }));
    await expect(new APIClient(() => "x").stream("/v1/chat/completions", { method: "POST", body: {} }, () => undefined)).rejects.toEqual(expect.objectContaining<Partial<APIError>>({ status: 502, code: "provider_failed", message: "No route" }));
  });

  it("rejects a successful non-SSE response", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200, headers: { "Content-Type": "application/json" } }));
    await expect(new APIClient(() => "x").stream("/v1/chat/completions", { method: "POST", body: {} }, () => undefined)).rejects.toEqual(expect.objectContaining<Partial<APIError>>({ code: "invalid_stream" }));
  });

  it("returns an explicitly accepted JSON fallback without replaying the request", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ id: "resp-fallback" }), { status: 200, headers: { "Content-Type": "application/json" } }));
    const result = await new APIClient(() => "x").stream<{ id: string }>("/v1/responses", { method: "POST", body: { stream: true } }, () => undefined, true);
    expect(result).toEqual({ streamed: false, data: { id: "resp-fallback" } });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("cancels and unlocks the response body when event handling fails", async () => {
    const encoder = new TextEncoder();
    let cancelled = false;
    const body = new ReadableStream<Uint8Array>({
      start(controller) { controller.enqueue(encoder.encode('data: {"broken":true}\n\n')); },
      cancel() { cancelled = true; }
    });
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } }));
    await expect(new APIClient(() => "x").stream("/v1/chat/completions", { method: "POST", body: {} }, () => { throw new Error("bad event"); })).rejects.toThrow("bad event");
    expect(cancelled).toBe(true);
    expect(body.locked).toBe(false);
  });
});
