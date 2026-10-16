import { readCollapsedGroups, saveCollapsedGroups, sidebarStorageKey } from "./sidebarState";

describe("sidebar preferences", () => {
  it("defaults to collapsed groups and stores only known boolean preferences", () => {
    expect(readCollapsedGroups()).toEqual({ models: true, agentic: true, mcp: true, tools: true, settings: true });
    saveCollapsedGroups({ models: false, agentic: true, mcp: false, tools: true, settings: false, unexpected: true });
    expect(readCollapsedGroups()).toEqual({ models: false, agentic: true, mcp: false, tools: true, settings: false });
    expect(window.localStorage.getItem(sidebarStorageKey)).not.toContain("unexpected");
  });
  it.each(["not-json", "null", "[]", '"value"', '{"models":"false","mcp":null,"unexpected":true}'])("ignores malformed storage: %s", (value) => {
    window.localStorage.setItem(sidebarStorageKey, value);
    expect(readCollapsedGroups().models).toBe(true);
  });
  it("remains usable when browser storage is unavailable or full", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("Blocked"); });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("Quota"); });
    expect(readCollapsedGroups().models).toBe(true);
    expect(() => saveCollapsedGroups({ models: false })).not.toThrow();
  });
});
