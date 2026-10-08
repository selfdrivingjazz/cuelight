import * as React from "react";
import { cn } from "./utils";

export type ToastTone = "success" | "error" | "info";
export interface ToastOptions { tone?: ToastTone; description?: string; /** Milliseconds; `null` persists until dismissed. */ duration?: number | null }
interface ToastRecord { id: number; title: string; tone: ToastTone; description?: string; duration: number | null }
export interface ToastApi {
  toast: (title: string, options?: ToastOptions) => number;
  success: (title: string, options?: Omit<ToastOptions, "tone">) => number;
  error: (title: string, options?: Omit<ToastOptions, "tone">) => number;
  dismiss: (id: number) => void;
}

const defaultDurations: Record<ToastTone, number | null> = { success: 4000, info: 4000, error: null };
const toneStyles: Record<ToastTone, string> = { success: "border-l-status-success", info: "border-l-status-info", error: "border-l-status-danger" };
const tallyStyles: Record<ToastTone, string> = { success: "bg-status-success", info: "bg-status-info", error: "bg-status-danger" };
const ToastContext = React.createContext<ToastApi | null>(null);

export function useToast(): ToastApi {
  const api = React.useContext(ToastContext);
  if (!api) throw new Error("useToast must be used within a ToastProvider");
  return api;
}

export function ToastProvider({ children, max = 4 }: { children: React.ReactNode; max?: number }) {
  const [toasts, setToasts] = React.useState<ToastRecord[]>([]);
  const nextId = React.useRef(0);
  const dismiss = React.useCallback((id: number) => setToasts((current) => current.filter((item) => item.id !== id)), []);
  const api = React.useMemo<ToastApi>(() => {
    const toast = (title: string, { tone = "success", description, duration }: ToastOptions = {}) => {
      const id = ++nextId.current;
      setToasts((current) => [...current, { id, title, tone, description, duration: duration === undefined ? defaultDurations[tone] : duration }].slice(-max));
      return id;
    };
    return { toast, dismiss, success: (title, options) => toast(title, { ...options, tone: "success" }), error: (title, options) => toast(title, { ...options, tone: "error" }) };
  }, [dismiss, max]);
  return <ToastContext.Provider value={api}>
    {children}
    <section aria-label="Notifications" className="pointer-events-none fixed inset-x-0 top-0 z-[60] flex flex-col gap-2 p-3 sm:bottom-0 sm:left-auto sm:top-auto sm:w-96 sm:flex-col-reverse sm:p-4">
      {toasts.map((item) => <ToastItem key={item.id} toast={item} onDismiss={() => dismiss(item.id)} />)}
    </section>
  </ToastContext.Provider>;
}

function ToastItem({ toast, onDismiss }: { toast: ToastRecord; onDismiss: () => void }) {
  const [paused, setPaused] = React.useState(false);
  React.useEffect(() => {
    if (toast.duration === null || paused) return;
    const timer = window.setTimeout(onDismiss, toast.duration);
    return () => window.clearTimeout(timer);
  }, [toast.duration, paused, onDismiss]);
  return <div
    role={toast.tone === "error" ? "alert" : "status"}
    aria-live={toast.tone === "error" ? "assertive" : "polite"}
    aria-atomic="true"
    onMouseEnter={() => setPaused(true)} onMouseLeave={() => setPaused(false)} onFocus={() => setPaused(true)} onBlur={() => setPaused(false)}
    onKeyDown={(event) => { if (event.key === "Escape") onDismiss(); }}
    className={cn("pointer-events-auto flex items-start gap-3 rounded-lg border border-l-4 border-border bg-card p-3 text-sm text-card-foreground shadow-lg motion-safe:animate-[toast-in_160ms_ease-out]", toneStyles[toast.tone])}
  >
    <span aria-hidden="true" className={cn("tally mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full", tallyStyles[toast.tone])} />
    <div className="min-w-0 flex-1"><p className="font-medium break-words">{toast.title}</p>{toast.description && <p className="mt-0.5 break-words text-muted-foreground">{toast.description}</p>}</div>
    <button type="button" onClick={onDismiss} aria-label="Dismiss notification" className="-m-1 rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
      <svg viewBox="0 0 24 24" className="h-4 w-4" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18" /></svg>
    </button>
  </div>;
}
