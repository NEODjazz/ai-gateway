import { useState } from "react";
import { PageHeader } from "../components/PageHeader";
import { AuditLogsPage } from "./AuditLogsPage";
import { RequestLogsPage } from "./RequestLogsPage";

type LogsTab = "requests" | "audit";

export function LogsPage() {
  const [tab, setTab] = useState<LogsTab>("requests");
  return <><PageHeader eyebrow="Observability" title="Logs" description="Request outcomes and administrative audit events in one operational workspace." /><div className="page-tabs" role="tablist" aria-label="Log types"><button role="tab" aria-selected={tab === "requests"} className={tab === "requests" ? "active" : ""} onClick={() => setTab("requests")}>Request Logs</button><button role="tab" aria-selected={tab === "audit"} className={tab === "audit" ? "active" : ""} onClick={() => setTab("audit")}>Audit Logs</button></div>{tab === "requests" ? <RequestLogsPage embedded /> : <AuditLogsPage />}</>;
}
