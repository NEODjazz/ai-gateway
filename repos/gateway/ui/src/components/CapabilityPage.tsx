import { PageHeader } from "./PageHeader";

export function CapabilityPage({ title, description, status = "Not supported by this gateway build", available = false }: { title: string; description: string; status?: string; available?: boolean }) {
  return <><PageHeader eyebrow="Capability" title={title} description={description} /><section className="capability-card"><span className={`status ${available ? "enabled" : "disabled"}`}>{available ? "Available" : "Unavailable"}</span><h2>{status}</h2><p>{available ? "This stable route documents an environment-managed capability." : "This route is explicit so navigation and permissions remain stable. No placeholder data or simulated backend behavior is shown."}</p></section></>;
}
