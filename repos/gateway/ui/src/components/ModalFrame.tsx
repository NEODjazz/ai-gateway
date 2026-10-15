import { useEffect, useRef, type ReactNode } from "react";
import { GravityThemeScope } from "./GravityThemeScope";
import { Modal, type ModalProps } from "@gravity-ui/uikit";

// All dialogs share focus containment/restoration and Escape dismissal. Outside
// clicks are disabled so a stray click does not discard an in-progress form.
export function ModalFrame({ label, onClose, children, dismissDisabled = false, returnFocus }: { label: string; onClose: () => void; children: ReactNode; dismissDisabled?: boolean; returnFocus?: ModalProps["returnFocus"] }) {
  const opener = useRef(document.activeElement instanceof HTMLElement ? document.activeElement : null);
  const target = typeof returnFocus === "object" ? returnFocus : opener;
  // A fast close can precede the library's focus manager. Restore only when
  // focus was lost, so another dialog or navigation keeps its chosen focus.
  useEffect(() => () => { queueMicrotask(() => { if (returnFocus !== false && target.current?.isConnected && document.activeElement === document.body) target.current.focus(); }); }, [returnFocus, target]);
  return <GravityThemeScope><Modal open aria-label={label} returnFocus={returnFocus ?? opener} disableOutsideClick disableEscapeKeyDown={dismissDisabled} disableVisuallyHiddenDismiss onOpenChange={(open) => { if (!open) onClose(); }}>{children}</Modal></GravityThemeScope>;
}
