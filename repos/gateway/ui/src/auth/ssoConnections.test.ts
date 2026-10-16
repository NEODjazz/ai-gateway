import { loginConnections, loginConnectionURL } from "./ssoConnections";

describe("SSO connection sign-in choices", () => {
  it("builds a local sign-in URL from validated metadata and ignores untrusted URLs", () => {
    const connections = loginConnections([{ id: "tenant-a", name: "Company", provider: "entra", organization_id: "org-a", start_url: "https://untrusted.example" }]);
    expect(loginConnectionURL(connections[0])).toBe("/auth/sso/start?connection=tenant-a");
    expect(connections[0]).not.toHaveProperty("start_url");
  });
  it.each([
    [{ id: "../tenant", name: "Company", provider: "oidc" }],
    [{ id: "a", name: "Company", provider: "oidc" }, { id: "a", name: "Other", provider: "oidc" }],
    [{ id: "a", name: "Company", provider: "other" }],
    [{ id: "a", name: "Company", provider: "oidc", organization_id: {} }],
    Array.from({ length: 18 }, (_, n) => ({ id: String(n), name: "Company", provider: "oidc" })),
  ].map((value) => ({ value })))("rejects malformed or ambiguous connection metadata %#", ({ value }) => {
    expect(() => loginConnections(value)).toThrow();
  });
  it("keeps the single-connection compatibility discovery valid", () => { expect(loginConnections(undefined)).toEqual([]); });
});
