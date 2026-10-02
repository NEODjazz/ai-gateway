import { useState } from "react";
import { GatewayButton } from "../../components/GatewayButton";

export function SSOPreset({ provider, disabled, onApply }: { provider: string; disabled: boolean; onApply: (value: { issuer: string; roles_claim: string; groups_claim: string }) => void }) {
  const [selected, setSelected] = useState(provider);
  const [tenant, setTenant] = useState("");
  const [base, setBase] = useState("");
  const [realm, setRealm] = useState("");
  const [error, setError] = useState("");
  function apply() {
    setError("");
    if (selected === "entra") {
      if (!/^[a-f0-9]{8}-(?:[a-f0-9]{4}-){3}[a-f0-9]{12}$/i.test(tenant)) { setError("Enter the specific Entra tenant ID (GUID). Shared tenant endpoints are not supported."); return; }
      onApply({ issuer: `https://login.microsoftonline.com/${tenant.toLowerCase()}/v2.0`, roles_claim: "roles", groups_claim: "groups" });
    } else if (selected === "keycloak") {
      try {
        const url = new URL(base);
        if (url.username || url.password || url.search || url.hash || (url.protocol !== "https:" && !(url.protocol === "http:" && ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname))) || !/^[a-zA-Z0-9_-]+$/.test(realm)) throw new Error();
        onApply({ issuer: `${url.href.replace(/\/$/, "")}/realms/${realm}`, roles_claim: "realm_access.roles", groups_claim: "groups" });
      } catch { setError("Enter a trusted HTTPS Keycloak base URL and a realm name (letters, digits, underscore or hyphen). HTTP is allowed only on loopback."); }
    }
  }
  return <fieldset disabled={disabled} className="form-grid" aria-label="Provider preset">
    <label>Provider preset<select value={selected} onChange={(event) => { setSelected(event.target.value); setError(""); }}><option value="entra">Microsoft Entra</option><option value="keycloak">Keycloak</option><option value="oidc">Generic OIDC</option></select></label>
    {selected === "entra" && <label>Entra tenant ID<input value={tenant} onChange={(event) => setTenant(event.target.value)} placeholder="Tenant GUID" /></label>}
    {selected === "keycloak" && <><label>Keycloak base URL<input value={base} onChange={(event) => setBase(event.target.value)} placeholder="https://identity.example" /></label><label>Keycloak realm<input value={realm} onChange={(event) => setRealm(event.target.value)} /></label></>}
    {selected !== "oidc" ? <><GatewayButton type="button" onClick={apply}>Apply preset</GatewayButton><p>Apply the issuer and claim paths, then discover endpoints. Group claims must be configured at your IdP. Secrets and existing mappings are preserved for review.</p></> : <p>Enter your issuer and exact role/group claim paths, then discover endpoints.</p>}
    {error && <p role="alert">{error}</p>}
  </fieldset>;
}
