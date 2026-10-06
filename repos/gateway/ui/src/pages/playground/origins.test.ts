import { assertPlaygroundOrigin, playgroundOrigins } from "./origins";

function metadata(content: string) {
  const node = document.createElement("meta"); node.name = "ai-gateway-playground-origins"; node.content = content; document.head.append(node); return node;
}
afterEach(() => document.querySelectorAll('meta[name="ai-gateway-playground-origins"]').forEach((node) => node.remove()));
describe("Playground browser origin trust", () => {
  it("defaults to same origin and fails before a foreign transport can receive a key", () => {
    expect(playgroundOrigins()).toEqual([]);
    expect(() => assertPlaygroundOrigin("")).not.toThrow();
    expect(() => assertPlaygroundOrigin(`${window.location.origin}/gateway/v1`)).not.toThrow();
    expect(() => assertPlaygroundOrigin("https://other.example.test/v1")).toThrow("not trusted");
  });
  it("uses exact server-provided origins including port, without trusting suffixes or sibling hosts", () => {
    metadata('["https://gateway.example.test","http://127.0.0.1:8081","https://gateway.example.test"]');
    expect(playgroundOrigins()).toEqual(["https://gateway.example.test", "http://127.0.0.1:8081"]);
    expect(() => assertPlaygroundOrigin("https://gateway.example.test/tenant/v1")).not.toThrow();
    expect(() => assertPlaygroundOrigin("http://127.0.0.1:8081/v1")).not.toThrow();
    for (const url of ["https://gateway.example.test.evil.test", "https://gateway.example.test:8443", "https://other.example.test", "http://127.0.0.1:8082"]) expect(() => assertPlaygroundOrigin(url)).toThrow("not trusted");
  });
  it.each(["broken", "{}", "null", '["*"]', '["https://*.example.test"]', '["http://example.test"]', '["https://user:secret@example.test"]', '["https://example.test/v1"]', '["https://example.test/"]', '["https://example.test",false]', JSON.stringify(Array(17).fill("https://example.test")), "x".repeat(8193)])("fails closed on malformed public metadata", (value) => {
    metadata(value); expect(playgroundOrigins()).toEqual([]); expect(() => assertPlaygroundOrigin("https://example.test")).toThrow("not trusted");
  });
  it("reports mixed content before transport even when an origin was configured", () => {
    metadata('["http://127.0.0.1:8081"]');
    const original = window.location;
    vi.stubGlobal("location", { ...original, origin: "https://console.example.test", protocol: "https:" });
    try { expect(() => assertPlaygroundOrigin("http://127.0.0.1:8081/v1")).toThrow("mixed-content"); }
    finally { vi.unstubAllGlobals(); }
  });
});
