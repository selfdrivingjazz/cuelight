import type { Config } from "tailwindcss";

export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}", "../../packages/controller-ui/src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: { background: "hsl(var(--background))", foreground: "hsl(var(--foreground))", primary: { DEFAULT: "hsl(var(--primary))", foreground: "hsl(var(--primary-foreground))" }, card: { DEFAULT: "hsl(var(--card))", foreground: "hsl(var(--card-foreground))" }, accent: "hsl(var(--accent))", border: "hsl(var(--border))", muted: { DEFAULT: "hsl(var(--muted))", foreground: "hsl(var(--muted-foreground))" }, ring: "hsl(var(--ring))", sidebar: "hsl(var(--card))", "status-success": "hsl(var(--status-success))", "status-warning": "hsl(var(--status-warning))", "status-danger": "hsl(var(--status-danger))", "status-info": "hsl(var(--status-info))" },
    },
  },
  plugins: [],
} satisfies Config;
