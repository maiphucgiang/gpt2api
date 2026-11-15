import { defineConfig, lazyPlugins } from "vite-plus";
import vue from "@vitejs/plugin-vue";
import path from "path";

const proxyTarget = process.env.VITE_DEV_API_TARGET || "http://localhost:8000";

// https://vite.dev/config/
export default defineConfig({
  fmt: {},
  lint: {
    jsPlugins: [{ name: "vite-plus", specifier: "vite-plus/oxlint-plugin" }],
    rules: { "vite-plus/prefer-vite-plus-imports": "error" },
    options: { typeAware: true, typeCheck: true },
  },
  plugins: lazyPlugins(() => [vue()]),
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  build: {
    outDir: path.resolve(__dirname, "dist"),
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: proxyTarget,
        changeOrigin: true,
      },
      "/auth": {
        target: proxyTarget,
        changeOrigin: true,
      },
      "/version": {
        target: proxyTarget,
        changeOrigin: true,
      },
      "/meta": {
        target: proxyTarget,
        changeOrigin: true,
      },
      "/health": {
        target: proxyTarget,
        changeOrigin: true,
      },
      "/images": {
        target: proxyTarget,
        changeOrigin: true,
      },
      "/image-thumbnails": {
        target: proxyTarget,
        changeOrigin: true,
      },
      "/v1": {
        target: proxyTarget,
        changeOrigin: true,
      },
    },
  },
});
