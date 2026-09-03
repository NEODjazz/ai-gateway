import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  base: "/ui/",
  plugins: [react()],
  build: {
    outDir: "../internal/gateway/adminui",
    emptyOutDir: true,
    assetsDir: "assets",
    rollupOptions: {
      output: {
        entryFileNames: "assets/app.js",
        chunkFileNames: "assets/[name]-[hash].js",
        assetFileNames: (asset) => asset.names.some((name) => name.endsWith(".css")) ? "assets/app.css" : "assets/[name]-[hash][extname]"
      }
    }
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: "./src/test/setup.ts",
    css: false,
    server: {
      deps: {
        inline: ["@gravity-ui/uikit", "@gravity-ui/icons"]
      }
    },
    coverage: {
      provider: "v8",
      reporter: ["text", "json-summary"],
      exclude: ["src/main.tsx", "src/pages/resourceConfigs.ts"],
      thresholds: { lines: 70, functions: 70, branches: 60, statements: 70 }
    }
  }
});
