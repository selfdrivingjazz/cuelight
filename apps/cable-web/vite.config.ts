import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

export default defineConfig({
  base: "/cable/",
  plugins: [react()],
  test: { include: ["src/**/*.test.{ts,tsx}"] },
});
