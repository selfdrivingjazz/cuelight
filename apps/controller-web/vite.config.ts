import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

const controllerProxy = process.env.VITE_CONTROLLER_PROXY;

export default defineConfig({
  plugins: [react()],
  server: controllerProxy ? { proxy: { "/api/v1": { target: controllerProxy, secure: false }, "/health": { target: controllerProxy, secure: false } } } : undefined,
  test: { include: ["src/**/*.test.{ts,tsx}"] },
});
