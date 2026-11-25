import { fileURLToPath, URL } from "url";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  logLevel: "error",
  define: {
    global: "globalThis",
  },
  build: {
    emptyOutDir: true,
    sourcemap: false,
    minify: "oxc",
    reportCompressedSize: false,
    rolldownOptions: {
      output: {
        manualChunks(id) {
          const p = id.replace(/\\/g, "/");
          if (p.endsWith("/src/lib/api.ts")) return "api";
          if (!p.includes("/node_modules/")) return;
          if (/\/node_modules\/(react|react-dom|scheduler)\//.test(p)) return "react";
          if (p.includes("/node_modules/@tanstack/")) return "tanstack";
        },
      },
    },
  },
  optimizeDeps: {
    rolldownOptions: {
      transform: { define: { global: "globalThis" } },
    },
  },
  server: {
    proxy: {
      "/api": {
        target: "http://localhost:8080",
        // Keep the browser's Host so the Go server's same-origin CSRF check
        // (Origin host must equal Host) passes for mutating /api/admin calls.
        changeOrigin: false,
      },
    },
  },
  plugins: [react()],
  resolve: {
    alias: [
      {
        find: "@",
        replacement: fileURLToPath(new URL("./src", import.meta.url)),
      },
    ],
  },
});
