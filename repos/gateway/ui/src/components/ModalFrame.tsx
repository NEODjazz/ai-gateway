import type { ReactNode } from "react";
import { GravityThemeScope } from "./GravityThemeScope";
import { Modal } from "@gravity-ui/uikit";

// All dialogs share focus containment/restoration and Escape dismissal. Outside
// clicks are disabled so a stray click does not discard an in-progress form.
export function ModalFrame({ label, onClose, children, dismissDisabled = false }: { label: string; onClose: () => void; children: ReactNode; dismissDisabled?: boolean }) {
  return <GravityThemeScope><Modal open aria-label={label} disableOutsideClick disableEscapeKeyDown={dismissDisabled} disableVisuallyHiddenDismiss onOpenChange={(open) => { if (!open) onClose(); }}>{children}</Modal></GravityThemeScope>;
}
