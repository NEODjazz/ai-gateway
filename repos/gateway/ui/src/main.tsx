import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { ThemeProvider } from "@gravity-ui/uikit";
import { App } from "./app/App";
import { AuthProvider } from "./auth/AuthContext";
import "@gravity-ui/uikit/styles/fonts.css";
import "@gravity-ui/uikit/styles/styles.css";
import "./styles.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ThemeProvider theme="light">
      <AuthProvider><App /></AuthProvider>
    </ThemeProvider>
  </StrictMode>,
);
