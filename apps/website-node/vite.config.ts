import { readFileSync } from "node:fs";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// Served by cuelight-control at /node/. TV browsers (Tizen, webOS, Silk) lag
// desktop engines, so syntax is lowered to ES2019 and no newer runtime APIs
// are used without feature detection.
export default defineConfig({
  base: "/node/",
  define: { __CUELIGHT_VERSION__: JSON.stringify(readFileSync(new URL("../../VERSION", import.meta.url), "utf8").trim()) },
  plugins: [react()],
  build: { target: "es2019", cssTarget: "chrome69" },
  test: { include: ["src/**/*.test.{ts,tsx}"] },
});
