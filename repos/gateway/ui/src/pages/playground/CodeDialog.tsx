import { useState } from "react";
import { GatewayButton } from "../../components/GatewayButton";
import { ModalFrame } from "../../components/ModalFrame";
import { PageTabs } from "../../components/PageTabs";
import { requestCode } from "./requests";

export function CodeDialog({ path, body, baseURL, onClose }: { path: string; body: unknown; baseURL: string; onClose: () => void }) {
  const [language, setLanguage] = useState<"curl" | "python" | "javascript">("curl");
  const [status, setStatus] = useState("");
  const code = requestCode(language, path, body, baseURL);
  async function copy() {
    try { await navigator.clipboard.writeText(code); setStatus("Copied"); }
    catch { setStatus("Copy failed. Select and copy the code manually."); }
  }
  return <ModalFrame label="Playground request code" onClose={onClose}>
    <section className="modal playground-code-dialog">
      <div className="modal-heading"><div><h2>Get code</h2><p>Set GATEWAY_API_KEY in your environment. Credentials are excluded from this example.</p></div><button type="button" className="icon-button" aria-label="Close code" onClick={onClose}>×</button></div>
      <PageTabs label="Code language" value={language} items={[{ value: "curl", label: "cURL" }, { value: "python", label: "Python" }, { value: "javascript", label: "JavaScript" }]} onUpdate={(value) => { setLanguage(value); setStatus(""); }} />
      <pre tabIndex={0} aria-label="Request code">{code}</pre>
      <p role="status">{status}</p>
      <div className="modal-actions"><GatewayButton view="outlined" onClick={onClose}>Close</GatewayButton><GatewayButton onClick={() => void copy()}>Copy code</GatewayButton></div>
    </section>
  </ModalFrame>;
}
