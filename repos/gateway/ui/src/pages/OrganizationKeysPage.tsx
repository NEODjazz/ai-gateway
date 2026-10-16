import { useEffect, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { DataTable, type Row } from "../components/DataTable";
import { GatewayButton } from "../components/GatewayButton";

type Key = Row & { id: string; organization_id?: string; alias?: string; user_id?: string; team_id?: string };
export function OrganizationKeysPage() {
  const { client, session } = useAuth();
  const [data, setData] = useState<{ data: Key[]; total: number } | null>(null);
  const [offset, setOffset] = useState(0);
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const organization = session?.organization_id || "";
  useEffect(() => {
    const controller = new AbortController(); setData(null); setError("");
    if (!organization) { setError("A verified organization is required."); return; }
    const query = new URLSearchParams({ organization_id: organization, limit: "25", offset: String(offset) });
    void client.request<{ data: Key[]; total: number }>(`/admin/v1/keys?${query}`, { signal: controller.signal }).then((page) => {
      if (controller.signal.aborted) return;
      if (!Array.isArray(page.data) || !Number.isSafeInteger(page.total) || page.total < 0 || page.data.some((key) => key.organization_id !== organization)) throw new Error("Key inventory does not match this organization.");
      setData(page);
    }).catch((cause) => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : "Could not load organization keys."); });
    return () => controller.abort();
  }, [client, organization, offset, reload]);
  return <><PageHeader eyebrow="Access control" title="Virtual keys" description={`Read-only key inventory for organization ${organization}. Key lifecycle changes require a platform administrator.`} />
    {error ? <ErrorState message={error} retry={() => setReload((n) => n + 1)} /> : !data ? <LoadingState /> : <>
      <DataTable rows={data.data} columns={[{ key: "id", label: "Key" }, { key: "alias", label: "Alias" }, { key: "user_id", label: "User" }, { key: "team_id", label: "Team" }, { key: "organization_id", label: "Organization" }, { key: "status", label: "Status" }, { key: "allowed_models", label: "Models" }]} />
      <div className="key-pagination"><span>{data.total ? `${offset + 1}–${offset + data.data.length} of ${data.total}` : "0 results"}</span><div className="page-actions"><GatewayButton disabled={offset === 0} onClick={() => setOffset((n) => Math.max(0, n - 25))}>Previous</GatewayButton><GatewayButton disabled={offset + 25 >= data.total} onClick={() => setOffset((n) => n + 25)}>Next</GatewayButton><GatewayButton onClick={() => setReload((n) => n + 1)}>Refresh</GatewayButton></div></div>
    </>}
  </>;
}
