import { IdentityDirectoryPage } from "./IdentityDirectoryPage";
import { ResourcePage } from "../components/ResourcePage";
import { resourceConfigs } from "./resourceConfigs";

export function TeamsPage() { return <IdentityDirectoryPage kind="teams" />; }
export function UsersPage() { return <IdentityDirectoryPage kind="users" />; }
export function OrganizationsPage() { return <ResourcePage config={resourceConfigs.organizations} />; }
