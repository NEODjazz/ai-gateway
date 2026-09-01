import { useSearchParams } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { AuditLogsPage } from "./AuditLogsPage";
import { RequestLogsPage } from "./RequestLogsPage";

type LogsTab = "requests" | "audit";

export function LogsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const requestedTab = searchParams.get("tab");
  const tab: LogsTab = requestedTab === "audit" ? "audit" : "requests";
  function selectTab(nextTab: LogsTab) {
    const next = new URLSearchParams(searchParams);
    if (nextTab === "requests") next.delete("tab");
    else next.set("tab", nextTab);
    setSearchParams(next);
  }
  return <><PageHeader eyebrow="Observability" title="Logs" description="Request outcomes and administrative audit events in one operational workspace." /><div className="page-tabs" role="tablist" aria-label="Log types"><button role="tab" aria-selected={tab === "requests"} className={tab === "requests" ? "active" : ""} onClick={() => selectTab("requests")}>Request Logs</button><button role="tab" aria-selected={tab === "audit"} className={tab === "audit" ? "active" : ""} onClick={() => selectTab("audit")}>Audit Logs</button></div>{tab === "requests" ? <RequestLogsPage embedded /> : <AuditLogsPage />}</>;
}
