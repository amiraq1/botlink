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
    minify: false,
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
        changeOrigin: true,
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
