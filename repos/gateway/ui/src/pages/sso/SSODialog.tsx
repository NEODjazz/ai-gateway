import { useState, type ReactNode } from "react";
import { GatewayButton } from "../../components/GatewayButton";
import { ModalCloseButton } from "../../components/ModalCloseButton";
import { ModalFrame } from "../../components/ModalFrame";

export function SSODialog({ title, busy, dirty = false, onClose, children }: { title: string; busy: boolean; dirty?: boolean; onClose: () => void; children: ReactNode }) {
  const [confirm, setConfirm] = useState(false);
  const close = () => { if (!busy) { if (dirty) setConfirm(true); else onClose(); } };
  return <ModalFrame label={title} dismissDisabled={busy} onClose={close}>
    <div className="modal sso-dialog">
      <div className="modal-heading"><h2>{title}</h2><ModalCloseButton label="Close dialog" disabled={busy} onClick={close} /></div>
      {confirm ? <div className="sso-discard" role="alert">
        <h3>Discard unsaved changes?</h3><p>Your saved configuration will remain unchanged.</p>
        <div className="page-actions"><GatewayButton view="normal" onClick={() => setConfirm(false)}>Continue editing</GatewayButton><GatewayButton onClick={onClose}>Discard changes</GatewayButton></div>
      </div> : <>{children}<div className="modal-actions"><GatewayButton view="normal" disabled={busy} onClick={close}>Cancel</GatewayButton></div></>}
    </div>
  </ModalFrame>;
}
