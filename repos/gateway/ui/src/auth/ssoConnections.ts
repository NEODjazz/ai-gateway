export type LoginConnection = { id: string; name: string; provider: string; organization_id?: string };
export function loginConnections(value: unknown): LoginConnection[] {
  if (value === undefined) return [];
  if (!Array.isArray(value) || value.length > 17) throw new Error("Invalid SSO connections.");
  const ids = new Set<string>();
  return value.map((entry: unknown) => {
    if (!entry || typeof entry !== "object") throw new Error("Invalid SSO connection.");
    const item = entry as Record<string, unknown>;
    if (typeof item.id !== "string" || !/^[a-z0-9_-]{1,64}$/.test(item.id) || ids.has(item.id) || typeof item.name !== "string" || !item.name || item.name.length > 128 || typeof item.provider !== "string" || !["entra", "keycloak", "oidc"].includes(item.provider) || (item.organization_id !== undefined && (typeof item.organization_id !== "string" || item.organization_id.length > 256))) throw new Error("Invalid SSO connection metadata.");
    ids.add(item.id);
    return { id: item.id, name: item.name, provider: item.provider, organization_id: item.organization_id as string | undefined };
  });
}
export const loginConnectionURL = (connection: LoginConnection) => `/auth/sso/start?connection=${encodeURIComponent(connection.id)}`;
