import { readFileSync } from "node:fs";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

export default defineConfig({
  base: "./",
  root: "src/renderer",
  plugins: [react()],
  define: { __CUELIGHT_VERSION__: JSON.stringify(readFileSync(new URL("../../VERSION", import.meta.url), "utf8").trim()) },
  build: { outDir: "../../dist", emptyOutDir: true },
  test: { include: ["../shared/**/*.test.ts", "../main/**/*.test.ts", "**/*.test.{ts,tsx}"] },
});
