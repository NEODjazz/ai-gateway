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
});
