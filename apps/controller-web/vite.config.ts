import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";
import type { ProxyOptions } from "vite";

const controllerProxy = process.env.VITE_CONTROLLER_PROXY;
const controllerToken = process.env.CABLE_CONTROLLER_DEV_OPERATOR_TOKEN;
const apiProxy: ProxyOptions | undefined = controllerProxy ? {
  target: controllerProxy,
  secure: false,
  configure(proxy) {
    if (!controllerToken) return;
    proxy.on("proxyReq", (request) => {
      if (!request.path.startsWith("/api/v1/remote/")) request.setHeader("Authorization", `Bearer ${controllerToken}`);
    });
  },
} : undefined;
export default defineConfig({
  plugins: [react()],
  server: {
    allowedHosts: ["cuelight.example.ts.net"],
    ...(apiProxy ? { proxy: { "/api/v1": apiProxy, "/health": { target: controllerProxy!, secure: false } } } : {}),
  },
  test: { include: ["src/**/*.test.{ts,tsx}"] },
});
