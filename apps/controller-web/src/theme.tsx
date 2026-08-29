import * as React from "react";

export type ControllerThemePreference = "light" | "dark" | "system";
type ResolvedTheme = Exclude<ControllerThemePreference, "system">;

interface ControllerThemeContextValue {
  preference: ControllerThemePreference;
  setPreference: (preference: ControllerThemePreference) => void;
}

const THEME_STORAGE_KEY = "cable.controller.theme";
const DARK_SCHEME_QUERY = "(prefers-color-scheme: dark)";
const ControllerThemeContext = React.createContext<ControllerThemeContextValue | null>(null);

function isThemePreference(value: string | null): value is ControllerThemePreference {
  return value === "light" || value === "dark" || value === "system";
}

function readStoredThemePreference(): ControllerThemePreference {
  if (typeof window === "undefined") return "system";
  try {
    const stored = window.localStorage.getItem(THEME_STORAGE_KEY);
    return isThemePreference(stored) ? stored : "system";
  } catch {
    return "system";
  }
}

function resolveTheme(preference: ControllerThemePreference): ResolvedTheme {
  if (preference !== "system") return preference;
  if (typeof window === "undefined") return "light";
  return window.matchMedia(DARK_SCHEME_QUERY).matches ? "dark" : "light";
}

function applyThemePreference(preference: ControllerThemePreference): void {
  if (typeof document === "undefined") return;
  const resolved = resolveTheme(preference);
  document.documentElement.dataset.theme = resolved;
  document.documentElement.style.colorScheme = resolved;
}

// Module evaluation precedes the app render and stylesheet import, so a saved
// preference is reflected by the first painted controller frame.
applyThemePreference(readStoredThemePreference());

export function ControllerThemeProvider({ children }: { children: React.ReactNode }) {
  const [preference, setPreference] = React.useState<ControllerThemePreference>(readStoredThemePreference);

  React.useEffect(() => {
    applyThemePreference(preference);

    try {
      window.localStorage.setItem(THEME_STORAGE_KEY, preference);
    } catch {
      // Theme selection still applies when storage is unavailable.
    }

    if (preference !== "system") return;
    const scheme = window.matchMedia(DARK_SCHEME_QUERY);
    const handleSchemeChange = () => applyThemePreference("system");
    scheme.addEventListener("change", handleSchemeChange);
    return () => scheme.removeEventListener("change", handleSchemeChange);
  }, [preference]);

  React.useEffect(() => {
    const handleStorage = (event: StorageEvent) => {
      if (event.key !== THEME_STORAGE_KEY) return;
      setPreference(isThemePreference(event.newValue) ? event.newValue : "system");
    };
    window.addEventListener("storage", handleStorage);
    return () => window.removeEventListener("storage", handleStorage);
  }, []);

  const value = React.useMemo(() => ({ preference, setPreference }), [preference]);
  return <ControllerThemeContext.Provider value={value}>{children}<ThemeControl /></ControllerThemeContext.Provider>;
}

export function useControllerTheme(): ControllerThemeContextValue {
  const value = React.useContext(ControllerThemeContext);
  if (!value) throw new Error("useControllerTheme must be used inside ControllerThemeProvider");
  return value;
}

function ThemeControl() {
  const { preference, setPreference } = useControllerTheme();
  return <div className="fixed bottom-4 right-4 z-30 flex items-center gap-2 rounded-md border border-border bg-card p-1.5 text-card-foreground shadow-sm">
    <label className="pl-1 text-xs font-medium text-muted-foreground" htmlFor="controller-theme">Theme</label>
    <select
      id="controller-theme"
      aria-label="Controller color theme"
      className="h-8 rounded-md border border-input bg-background px-2 text-xs font-medium text-foreground focus-visible:ring-2 focus-visible:ring-ring"
      value={preference}
      onChange={(event) => setPreference(event.currentTarget.value as ControllerThemePreference)}
    >
      <option value="system">System</option>
      <option value="light">Light</option>
      <option value="dark">Dark</option>
    </select>
  </div>;
}
