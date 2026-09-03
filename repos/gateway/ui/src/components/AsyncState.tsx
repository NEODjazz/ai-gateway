import type { PropsWithChildren } from "react";
import { Loader } from "@gravity-ui/uikit";
import { GatewayButton } from "./GatewayButton";
import { GravityThemeScope } from "./GravityThemeScope";

export function LoadingState() {
  return <div className="state-card" role="status"><GravityThemeScope className="gravity-loader-scope"><Loader size="m" /></GravityThemeScope>Loading…</div>;
}

export function ErrorState({ message, retry }: { message: string; retry?: () => void }) {
  return <div className="state-card error-state" role="alert"><p>{message}</p>{retry && <GatewayButton size="m" onClick={retry}>Retry</GatewayButton>}</div>;
}

export function EmptyState({ children }: PropsWithChildren) {
  return <div className="state-card empty-state">{children}</div>;
}
