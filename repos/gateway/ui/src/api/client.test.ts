import { APIClient, APIError } from "./client";

describe("APIClient", () => {
  it("sends bounded pre-encoded JSON without rounding numeric literals or quoting the JSON document", async () => {
    const body = '{"arguments":{"id":9007199254740993,"decimal":0.1234567890123456789012345}}';
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('{"ok":true}'));
    const client = new APIClient(() => "test-key", { credentials: "omit", sessionEvents: false });
    expect(await client.requestJSONText("/test", body, { method: "POST", maximumResponseBytes: 32 })).toBe('{"ok":true}');
    const options = mock.mock.calls[0][1]!;
    expect(options.body).toBe(body); expect(options.credentials).toBe("omit");
    expect(new Headers(options.headers).get("Content-Type")).toBe("application/json");
    expect(new Headers(options.headers).get("Authorization")).toBe("Bearer test-key");
    expect(options).not.toHaveProperty("maximumResponseBytes");
    mock.mockResolvedValue(new Response('{"id":9007199254740993}'));
    expect(await client.requestJSONText("/test", body)).toBe('{"id":9007199254740993}');
    mock.mockResolvedValue(new Response('"too long"'));
    await expect(client.requestJSONText("/test", body, { maximumResponseBytes: 4 })).rejects.toMatchObject({ code: "response_too_large" });
    mock.mockResolvedValue(new Response("broken"));
    await expect(client.requestJSONText("/test", body)).rejects.toThrow();
    mock.mockResolvedValue(new Response(null, { status: 204 }));
    expect(await client.requestJSONText("/test", body)).toBeUndefined();
    mock.mockResolvedValue(new Response('{"error":{"code":"denied","message":"Denied"}}', { status: 403 }));
    await expect(client.requestJSONText("/test", body)).rejects.toMatchObject({ code: "denied", status: 403 });
  });
  it.each(["", "[", "undefined", '{"id":NaN}', '"' + "я".repeat(524288) + '"'])("rejects invalid or oversized pre-encoded JSON before credential or transport use", async (body) => {
    const mock = vi.spyOn(globalThis, "fetch"), credential = vi.fn(() => "test-key");
    await expect(new APIClient(credential).requestJSONText("/test", body)).rejects.toThrow(/JSON request/);
    expect(mock).not.toHaveBeenCalled(); expect(credential).not.toHaveBeenCalled();
  });
  it("bounds JSON and binary responses before buffering oversized content", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async () => new Response('"oversized"'));
    const client = new APIClient(() => "test-key");
    await expect(client.request("/test", { maximumResponseBytes: 4 })).rejects.toMatchObject({ code: "response_too_large" });
    await expect(client.requestBinary("/test", { maximumResponseBytes: 4 })).rejects.toMatchObject({ code: "response_too_large" });
    expect(fetchMock.mock.calls[0][1]).not.toHaveProperty("maximumResponseBytes");
  });
  it("cancels an oversized declared response before reading it", async () => {
    const cancel = vi.fn();
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(new ReadableStream({ cancel }), { headers: { "Content-Length": "100" } }));
    await expect(new APIClient(() => "").request("/test", { maximumResponseBytes: 10 })).rejects.toMatchObject({ code: "response_too_large" });
    expect(cancel).toHaveBeenCalledOnce();
  });
  it("bounds streamed wire bytes and JSON fallback responses", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('data: {"text":"too long"}\n\n', { headers: { "Content-Type": "text/event-stream" } }));
    const client = new APIClient(() => ""), callback = vi.fn();
    await expect(client.stream("/test", { maximumResponseBytes: 4 }, callback, true)).rejects.toMatchObject({ code: "response_too_large" });
    expect(callback).not.toHaveBeenCalled();
    mock.mockResolvedValue(new Response('{"text":"too long"}', { headers: { "Content-Type": "application/json" } }));
    await expect(client.stream("/test", { maximumResponseBytes: 4 }, callback, true)).rejects.toMatchObject({ code: "response_too_large" });
  });
  it("sends a JSON request and receives binary audio without decoding it as JSON", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(new Uint8Array([0, 255, 17]), { status: 200, headers: { "Content-Type": "audio/mpeg" } }));
    const result = await new APIClient(() => "inference-key", { credentials: "omit", sessionEvents: false }).requestBinary("/v1/audio/speech", { method: "POST", body: { model: "speech", input: "Hello", voice: "alloy" } });
    expect(result.contentType).toBe("audio/mpeg");
    const bytes = await new Promise<ArrayBuffer>((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () => resolve(reader.result as ArrayBuffer);
      reader.onerror = () => reject(reader.error);
      reader.readAsArrayBuffer(result.body);
    });
    expect(new Uint8Array(bytes)).toEqual(new Uint8Array([0, 255, 17]));
    const options = fetchMock.mock.calls[0][1]!;
    expect(options.credentials).toBe("omit");
    expect(options.method).toBe("POST");
    expect(JSON.parse(String(options.body))).toEqual({ model: "speech", input: "Hello", voice: "alloy" });
    expect(new Headers(options.headers).get("Accept")).toBe("application/octet-stream");
  });

  it.each([401, 409])("keeps an independent test credential failure (%s) from changing the console session", async (status) => {
    const expired = vi.fn(), conflict = vi.fn();
    window.addEventListener("control-plane-session-expired", expired);
    window.addEventListener("control-plane-conflict", conflict);
    try {
      vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ error: { code: status === 401 ? "unauthorized" : "revision_conflict", message: "Test credential failed" } }), { status }));
      await expect(new APIClient(() => "test-key", { sessionEvents: false }).request("/v1/models")).rejects.toThrow("Test credential failed");
      expect(expired).not.toHaveBeenCalled(); expect(conflict).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener("control-plane-session-expired", expired);
      window.removeEventListener("control-plane-conflict", conflict);
    }
  });

  it("uses the same bounded API error for a failed binary request", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ error: { code: "provider_failed", message: "Speech unavailable" } }), { status: 502 }));
    await expect(new APIClient(() => "x").requestBinary("/v1/audio/speech", { method: "POST", body: {} })).rejects.toThrow("Speech unavailable");
  });

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

  it("uploads forms without overriding the multipart boundary and downloads binary responses", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: "skill_a" }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response("archive", { status: 200, headers: { "Content-Type": "application/zip" } }));
    const client = new APIClient(() => "token");
    const form = new FormData(); form.append("files", new File(["content"], "SKILL.md"));
    await expect(client.requestForm<{ id: string }>("/v1/skills", form, { method: "POST" })).resolves.toEqual({ id: "skill_a" });
    const uploadHeaders = new Headers(fetchMock.mock.calls[0][1]?.headers);
    expect(uploadHeaders.get("Authorization")).toBe("Bearer token");
    expect(uploadHeaders.has("Content-Type")).toBe(false);
    expect(fetchMock.mock.calls[0][1]?.body).toBe(form);
    const downloaded = await client.download("/v1/skills/skill_a/content");
    expect(downloaded.contentType).toBe("application/zip");
    expect(downloaded.body).toBeInstanceOf(Blob);
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
