import { navigationSections } from "./navigation";

export const sidebarStorageKey = "ai-gateway.sidebar-collapsed.v1";
export type CollapsedGroups = Record<string, boolean>;
const groupIDs = navigationSections.flatMap((section) => section.items.flatMap((item) => typeof item === "string" ? [] : [item.id]));

export function readCollapsedGroups(): CollapsedGroups {
  const defaults = Object.fromEntries(groupIDs.map((id) => [id, true]));
  try {
    const stored: unknown = JSON.parse(window.localStorage.getItem(sidebarStorageKey) || "null");
    if (stored && typeof stored === "object" && !Array.isArray(stored)) {
      for (const id of groupIDs) {
        const value = (stored as Record<string, unknown>)[id];
        if (typeof value === "boolean") defaults[id] = value;
      }
    }
  } catch { /* Browser storage is optional; navigation remains usable. */ }
  return defaults;
}

export function saveCollapsedGroups(groups: CollapsedGroups): void {
  try { window.localStorage.setItem(sidebarStorageKey, JSON.stringify(Object.fromEntries(groupIDs.map((id) => [id, Boolean(groups[id])])))); }
  catch { /* Private browsing or storage quotas must not block navigation. */ }
}
