import { useState } from "react";
import { GatewayButton } from "../../components/GatewayButton";
import { ModalFrame } from "../../components/ModalFrame";
import { PageTabs } from "../../components/PageTabs";
import { requestCode, type CodeCheck } from "./requests";

export function CodeDialog({ path, body, baseURL, headers, binaryOutput, checks, onClose }: { path: string; body: unknown; baseURL: string; headers?: Record<string, string>; binaryOutput?: boolean; checks?: CodeCheck[]; onClose: () => void }) {
  const [language, setLanguage] = useState<"curl" | "python" | "javascript">("curl");
  const [status, setStatus] = useState("");
  const code = requestCode(language, path, body, baseURL, headers, binaryOutput, checks);
  async function copy() {
    try { await navigator.clipboard.writeText(code); setStatus("Copied"); }
    catch { setStatus("Copy failed. Select and copy the code manually."); }
  }
  function download() {
    const url = URL.createObjectURL(new Blob([code], { type: "text/plain;charset=utf-8" }));
    const link = document.createElement("a"); link.href = url;
    link.download = `ai-gateway-request.${language === "curl" ? "sh" : language === "python" ? "py" : "mjs"}`;
    link.click(); URL.revokeObjectURL(url);
  }
  return <ModalFrame label="Playground request code" onClose={onClose}>
    <section className="modal playground-code-dialog">
      <div className="modal-heading"><div><h2>Get code</h2><p>Set GATEWAY_API_KEY in your environment. Credentials are excluded from this example.</p>{!!checks?.length && <p>This example requires each selected prompt policy to allow generation before sending the inference request.</p>}</div><button type="button" className="icon-button" aria-label="Close code" onClick={onClose}>×</button></div>
      <PageTabs label="Code language" value={language} items={[{ value: "curl", label: "cURL" }, { value: "python", label: "Python" }, { value: "javascript", label: "JavaScript" }]} onUpdate={(value) => { setLanguage(value); setStatus(""); }} />
      <pre tabIndex={0} aria-label="Request code">{code.slice(0, 65536)}</pre>
      {code.length > 65536 && <p>Preview is limited to 64 KiB. Copy or download the complete request, including inline attachments.</p>}
      <p role="status">{status}</p>
      <div className="modal-actions"><GatewayButton view="outlined" onClick={onClose}>Close</GatewayButton><GatewayButton view="outlined" onClick={download}>Download code</GatewayButton><GatewayButton onClick={() => void copy()}>Copy code</GatewayButton></div>
    </section>
  </ModalFrame>;
}
