import { useCallback, useEffect, useRef, useState } from "react";
import { useAuth } from "../../auth/AuthContext";
import type { BudgetSummary } from "../budgets/BudgetForm";
import { isOrganization, type OrganizationMetadata } from "./OrganizationPicker";

export type IdentitySummary = {
  id: string;
  directory: { id: string; key_count: number; organization: OrganizationMetadata | null } | null;
  policy: { model_access: "inherit" | "deny" | "allowlist"; allowed_models: string[]; model_count: number; rpm: number; tpm: number } | null;
  budgets: { id: string; total: number; data: BudgetSummary[] } | null;
  spend: { id: string; currency: string; cost: number; requests: number }[] | null;
};
type Snapshot = { scope_type: string; from: string; to: string; data: IdentitySummary[]; errors: Record<string, string> };
const count = (value: number) => Number.isSafeInteger(value) && value >= 0;
const amount = (value: number) => Number.isFinite(value) && value >= 0;
const currency = (value: string) => /^[A-Z]{3}$/.test(value);
function valid(value: Snapshot, kind: string, ids: string[]) {
  if (!value || value.scope_type !== kind || !Number.isFinite(Date.parse(value.from)) || !Number.isFinite(Date.parse(value.to)) || Date.parse(value.to) <= Date.parse(value.from) || !Array.isArray(value.data) || value.data.length !== ids.length || !value.errors || Object.values(value.errors).some((error) => typeof error !== "string")) return false;
  const expected = new Set(ids), seen = new Set<string>();
  return value.data.every((row) => {
    if (!expected.has(row.id) || seen.has(row.id)) return false;
    seen.add(row.id);
    const d = row.directory, p = row.policy, b = row.budgets, s = row.spend;
    if (d !== null && (!d || d.id !== row.id || !count(d.key_count) || !(d.organization === null || isOrganization(d.organization)))) return false;
    if (p !== null && (!p || !["inherit", "deny", "allowlist"].includes(p.model_access) || !Array.isArray(p.allowed_models) || p.allowed_models.some((model) => typeof model !== "string") || !count(p.model_count) || p.allowed_models.length !== Math.min(2, p.model_count) || (p.model_access === "allowlist") !== (p.model_count > 0) || !count(p.rpm) || !count(p.tpm))) return false;
    if (b !== null && (!b || b.id !== row.id || !count(b.total) || !Array.isArray(b.data) || b.data.length !== Math.min(2, b.total) || b.data.some((summary) => !summary?.policy || summary.policy.scope_type !== kind || summary.policy.scope_id !== row.id || !count(summary.policy.id) || !currency(summary.policy.currency) || !amount(summary.used_cost) || !count(summary.used_tokens) || summary.policy.max_cost !== undefined && !amount(summary.policy.max_cost) || summary.policy.max_tokens !== undefined && !count(summary.policy.max_tokens) || !Number.isFinite(Date.parse(summary.window_end))))) return false;
    return s === null || Array.isArray(s) && new Set(s.map((item) => item.currency)).size === s.length && s.every((item) => item.id === row.id && currency(item.currency) && amount(item.cost) && count(item.requests) && item.requests > 0);
  });
}

export function useIdentitySummaries(kind: "team" | "user", scope: string, page: { data: { id: string }[] } | undefined, enabled: boolean) {
  const { client } = useAuth();
  const idsJSON = JSON.stringify(page?.data.map((row) => row.id) || []);
  const key = JSON.stringify([scope, kind, idsJSON]);
  const active = useRef(key); active.current = key;
  const generation = useRef(0);
  const pending = useRef<AbortController | undefined>(undefined);
  const [state, setState] = useState<{ key: string; value?: Snapshot; loading: boolean; error: string }>();
  const reload = useCallback(async () => {
    const ids = JSON.parse(idsJSON) as string[];
    if (!enabled || !ids.length || active.current !== key) return;
    const current = ++generation.current;
    pending.current?.abort(); const controller = new AbortController(); pending.current = controller;
    setState((value) => ({ key, value: value?.key === key ? value.value : undefined, loading: true, error: "" }));
    try {
      const value = await client.request<Snapshot>("/admin/v1/directory-summaries", { method: "POST", body: { scope_type: kind, ids }, signal: controller.signal, maximumResponseBytes: 2 * 1024 * 1024 });
      if (!valid(value, kind, ids)) throw new Error("Directory summaries returned invalid or mismatched data.");
      if (active.current === key && current === generation.current) setState({ key, value, loading: false, error: "" });
    } catch (cause) {
      if (active.current === key && current === generation.current && !controller.signal.aborted) setState((value) => ({ key, value: value?.key === key ? value.value : undefined, loading: false, error: cause instanceof Error ? cause.message : "Could not load directory summaries." }));
    }
  }, [client, enabled, idsJSON, key, kind, page]);
  useEffect(() => { void reload(); return () => { ++generation.current; pending.current?.abort(); }; }, [reload]);
  const current = enabled && state?.key === key ? state : undefined;
  return { value: current?.value, loading: enabled && Boolean(page?.data.length) && (!current || current.loading), error: current?.error, reload };
}
