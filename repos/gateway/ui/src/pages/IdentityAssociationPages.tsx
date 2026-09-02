import { useAuth } from "../auth/AuthContext";
import { ResourcePage } from "../components/ResourcePage";
import { resourceConfigs } from "./resourceConfigs";

export function TeamsPage() {
  const { hasCapability } = useAuth();
  return <ResourcePage config={resourceConfigs.teams} allowCreate={hasCapability("admin")} />;
}
export function OrganizationsPage() { return <ResourcePage config={resourceConfigs.organizations} />; }
