import { ThemeProvider } from "@gravity-ui/uikit";
import type { ReactNode } from "react";

export function GravityThemeScope({ children, className = "" }: { children: ReactNode; className?: string }) {
  return <ThemeProvider theme="light" scoped rootClassName={className}>{children}</ThemeProvider>;
}
