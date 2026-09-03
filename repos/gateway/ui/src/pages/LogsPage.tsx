import { useSearchParams } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { PageTabs } from "../components/PageTabs";
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
  return <><PageHeader eyebrow="Observability" title="Logs" description="Request outcomes and administrative audit events in one operational workspace." /><PageTabs label="Log types" value={tab} items={[{ value: "requests", label: "Request Logs" }, { value: "audit", label: "Audit Logs" }]} onUpdate={selectTab} />{tab === "requests" ? <RequestLogsPage embedded /> : <AuditLogsPage />}</>;
}
