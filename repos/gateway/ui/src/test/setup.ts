import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, vi } from "vitest";

class ResizeObserverMock implements ResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

globalThis.ResizeObserver = ResizeObserverMock;
// Node's optional Web Storage must not shadow the isolated jsdom storage.
const testDOM = globalThis as typeof globalThis & { jsdom: { window: Window } };
Object.defineProperty(globalThis, "localStorage", { configurable: true, value: testDOM.jsdom.window.localStorage });
window.matchMedia = (query: string) => ({ matches: false, media: query, onchange: null, addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {}, dispatchEvent: () => true });

afterEach(() => {
  cleanup();
  sessionStorage.clear();
  window.localStorage.clear();
  history.replaceState({}, "", "/");
  vi.restoreAllMocks();
});
