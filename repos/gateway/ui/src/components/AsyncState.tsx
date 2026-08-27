import type { PropsWithChildren } from "react";

export function LoadingState() {
  return <div className="state-card" role="status"><span className="spinner" />Loading…</div>;
}

export function ErrorState({ message, retry }: { message: string; retry?: () => void }) {
  return <div className="state-card error-state" role="alert"><p>{message}</p>{retry && <button onClick={retry}>Retry</button>}</div>;
}

export function EmptyState({ children }: PropsWithChildren) {
  return <div className="state-card empty-state">{children}</div>;
}
