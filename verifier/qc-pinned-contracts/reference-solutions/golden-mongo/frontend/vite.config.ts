import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    // The API and the local map assets are served by the application itself, on
    // the loopback address the manifest publishes. The dev server must proxy to
    // that origin and never to itself.
    proxy: {
      "/api": "http://127.0.0.1:18082",
      "/map-assets": "http://127.0.0.1:18082",
    },
  },
});
