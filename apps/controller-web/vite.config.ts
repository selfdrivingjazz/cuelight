import { readFileSync } from "node:fs";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";
import type { ProxyOptions } from "vite";

const controllerProxy = process.env.VITE_CONTROLLER_PROXY;
const controllerToken = process.env.CUELIGHT_CONTROLLER_DEV_OPERATOR_TOKEN;
const apiProxy: ProxyOptions | undefined = controllerProxy ? {
  target: controllerProxy,
  secure: false,
  configure(proxy) {
    if (!controllerToken) return;
    proxy.on("proxyReq", (request) => {
      if (!request.path.startsWith("/api/v1/remote/") && !request.path.startsWith("/api/v1/browser-node/") && !request.path.startsWith("/api/v1/app/")) request.setHeader("Authorization", `Bearer ${controllerToken}`);
    });
  },
} : undefined;
export default defineConfig({
  plugins: [react()],
  define: { __CUELIGHT_VERSION__: JSON.stringify(readFileSync(new URL("../../VERSION", import.meta.url), "utf8").trim()) },
  server: {
    // Extra dev-server hostnames (e.g. a tailnet name), comma-separated.
    allowedHosts: (process.env.CUELIGHT_DEV_ALLOWED_HOSTS ?? "").split(",").map((host) => host.trim()).filter(Boolean),
    // The control plane answers CORS itself (the /api/v1/app API allows any origin).
    cors: false,
    ...(apiProxy ? { proxy: { "/api/v1": apiProxy, "/health": { target: controllerProxy!, secure: false }, "^/node(/|$)": { target: controllerProxy!, secure: false } } } : {}),
  },
  test: { include: ["src/**/*.test.{ts,tsx}"] },
});
