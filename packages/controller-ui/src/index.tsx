import * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import * as Dialog from "@radix-ui/react-dialog";
import { cva, type VariantProps } from "class-variance-authority";
import { flexRender, getCoreRowModel, useReactTable, type ColumnDef } from "@tanstack/react-table";
import { cn } from "./utils";
export { ToastProvider, useToast, type ToastApi, type ToastOptions, type ToastTone } from "./toast";

const buttonStyles = cva("inline-flex shrink-0 items-center justify-center gap-2 whitespace-nowrap rounded-md text-sm font-medium transition-[background-color,border-color,color,box-shadow] focus-visible:outline-none disabled:pointer-events-none disabled:opacity-40 [&_svg]:h-4 [&_svg]:w-4", {
  variants: {
    variant: {
      primary: "bg-primary text-primary-foreground shadow-[inset_0_-1px_0_hsl(var(--shadow)/.25)] hover:bg-primary/90",
      secondary: "border border-input bg-card text-foreground hover:border-foreground/30 hover:bg-accent",
      ghost: "text-muted-foreground hover:bg-accent hover:text-foreground",
      danger: "border border-status-danger/50 bg-status-danger/10 text-status-danger hover:bg-status-danger/20",
    },
    size: { sm: "h-8 px-3 text-[13px]", md: "h-9 px-4", lg: "h-11 px-5", icon: "h-9 w-9" },
  },
  defaultVariants: { variant: "primary", size: "md" },
});

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement>, VariantProps<typeof buttonStyles> { asChild?: boolean }
export function Button({ className, variant, size, asChild = false, ...props }: ButtonProps) {
  const Comp = asChild ? Slot : "button";
  return <Comp className={cn(buttonStyles({ variant, size }), className)} {...props} />;
}

export function Card({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) { return <section className={cn("min-w-0 rounded-lg border border-border bg-card text-card-foreground", className)} {...props} />; }
export function CardHeader({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) { return <div className={cn("flex flex-col gap-1 px-5 pb-3 pt-4", className)} {...props} />; }
export function CardTitle({ className, ...props }: React.HTMLAttributes<HTMLHeadingElement>) { return <h2 className={cn("eyebrow text-foreground/80", className)} {...props} />; }
export function CardContent({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) { return <div className={cn("px-5 pb-5", className)} {...props} />; }

export type StatusTone = "success" | "warning" | "danger" | "neutral" | "info";
const statusStyles: Record<StatusTone, string> = { success: "border-status-success/35 text-status-success", warning: "border-status-warning/40 text-status-warning", danger: "border-status-danger/40 text-status-danger", neutral: "border-border text-muted-foreground", info: "border-status-info/40 text-status-info" };
export function StatusBadge({ tone, children, className }: { tone: StatusTone; children: React.ReactNode; className?: string }) { return <span className={cn("inline-flex items-center gap-1.5 rounded-[4px] border bg-card px-1.5 py-0.5 font-mono text-[11px] font-medium uppercase tracking-[0.08em]", statusStyles[tone], className)}><span aria-hidden="true" className="tally h-1.5 w-1.5 rounded-full bg-current" />{children}</span>; }

export function Skeleton({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) { return <div role="status" aria-label="Loading" className={cn("animate-pulse rounded-md bg-muted", className)} {...props} />; }
export function EmptyState({ title, description, action }: { title: string; description: string; action?: React.ReactNode }) { return <div className="flex min-h-44 flex-col items-center justify-center gap-2 rounded-lg border border-dashed border-input bg-[repeating-linear-gradient(135deg,transparent_0_10px,hsl(var(--muted)/.5)_10px_11px)] p-8 text-center"><h3 className="font-semibold">{title}</h3><p className="max-w-md text-sm text-muted-foreground">{description}</p>{action && <div className="mt-2">{action}</div>}</div>; }
export function ErrorState({ title = "Something went wrong", description, onRetry }: { title?: string; description: string; onRetry?: () => void }) { return <div role="alert" className="rounded-lg border border-status-danger/40 border-l-4 bg-status-danger/5 p-5"><h3 className="font-semibold text-status-danger">{title}</h3><p className="mt-1 text-sm text-muted-foreground">{description}</p>{onRetry && <Button className="mt-3" variant="secondary" size="sm" onClick={onRetry}>Try again</Button>}</div>; }

export type NavigationLink = React.ComponentType<{ to: string; className?: string; children?: React.ReactNode; "aria-current"?: "page" | undefined; onClick?: () => void }>;
const AnchorLink: NavigationLink = ({ to, ...props }) => <a href={to} {...props} />;
export interface Crumb { label: string; href?: string }
/** Full trail from `sm` up; on phones only a "← parent" link (or the current page when the parent is not navigable). */
export function Breadcrumbs({ items, linkComponent: LinkComponent = AnchorLink }: { items: readonly Crumb[]; linkComponent?: NavigationLink }) {
  const backIndex = items.length > 1 && items[items.length - 2]?.href ? items.length - 2 : -1;
  const compactIndex = backIndex >= 0 ? backIndex : items.length - 1;
  return <nav aria-label="Breadcrumb" className="min-w-0 text-sm"><ol className="flex min-w-0 items-center gap-1.5">{items.map((item, index) => {
    const last = index === items.length - 1;
    return <li key={`${item.label}-${index}`} className={cn("min-w-0 items-center gap-1.5", index === compactIndex ? "flex" : "hidden sm:flex")}>
      {index > 0 && <span aria-hidden="true" className="hidden text-input sm:inline">/</span>}
      {item.href && !last ? <LinkComponent className="truncate text-muted-foreground hover:text-foreground" to={item.href}>{index === backIndex && <span aria-hidden="true" className="mr-1 sm:hidden">←</span>}{item.label}</LinkComponent> : <span aria-current={last ? "page" : undefined} className={cn("truncate", last ? "font-medium text-foreground" : "text-muted-foreground")}>{item.label}</span>}
    </li>;
  })}</ol></nav>;
}

export function PageHeader({ title, description, eyebrow, meta, children }: { title: string; description?: string; eyebrow?: React.ReactNode; meta?: React.ReactNode; children?: React.ReactNode }) {
  return <header className="flex flex-wrap items-end justify-between gap-x-6 gap-y-4 border-b border-border pb-5">
    <div className="min-w-0 max-w-3xl flex-1 basis-64">
      {eyebrow && <p className="eyebrow mb-2 text-primary">{eyebrow}</p>}
      <h1 className="break-words text-[1.7rem] font-semibold leading-tight tracking-[-0.02em] md:text-[2rem]">{title}</h1>
      {description && <p className="mt-1.5 text-sm leading-relaxed text-muted-foreground">{description}</p>}
      {meta && <div className="mt-3 flex flex-wrap items-center gap-2">{meta}</div>}
    </div>
    {children && <div className="flex max-w-full flex-wrap items-center gap-2">{children}</div>}
  </header>;
}

export function PageFrame({ children, width = "wide" }: { children: React.ReactNode; width?: "wide" | "narrow" }) {
  return <main id="main-content" tabIndex={-1} className={cn("mx-auto w-full space-y-6 px-4 pb-16 pt-6 outline-none sm:px-6 md:px-8 md:pt-8", width === "narrow" ? "max-w-3xl" : "max-w-[var(--content-max-width)]")}>
    {children}
  </main>;
}

export interface SectionTab { label: string; href: string; active: boolean; count?: number }
export function SectionTabs({ label, tabs, linkComponent: LinkComponent = AnchorLink }: { label: string; tabs: readonly SectionTab[]; linkComponent?: NavigationLink }) {
  return <nav aria-label={label} className="-mt-2 flex gap-1 overflow-x-auto border-b border-border [scrollbar-width:none]">{tabs.map((tab) => <LinkComponent key={tab.href} to={tab.href} aria-current={tab.active ? "page" : undefined} className={cn("relative -mb-px whitespace-nowrap border-b-2 px-3 pb-2.5 pt-1 text-sm transition-colors", tab.active ? "border-primary font-medium text-foreground" : "border-transparent text-muted-foreground hover:text-foreground")}>{tab.label}{tab.count !== undefined && <span className="ml-1.5 font-mono text-xs text-muted-foreground">{tab.count}</span>}</LinkComponent>)}</nav>;
}

export interface Fact { label: React.ReactNode; value: React.ReactNode; mono?: boolean; wide?: boolean }
export function Facts({ items, columns = 2, className }: { items: readonly Fact[]; columns?: 1 | 2 | 3; className?: string }) {
  return <dl className={cn("grid gap-x-6 gap-y-4 text-sm", columns === 2 && "sm:grid-cols-2", columns === 3 && "sm:grid-cols-2 lg:grid-cols-3", className)}>{items.map((item, index) => <div key={index} className={cn("min-w-0", item.wide && "sm:col-span-full")}><dt className="eyebrow">{item.label}</dt><dd className={cn("mt-1 break-words text-foreground", item.mono && "break-all font-mono text-[13px]")}>{item.value}</dd></div>)}</dl>;
}

const tableShell = "overflow-x-auto rounded-lg border border-border bg-card";
const tableHead = "border-b border-border text-left";
const headCell = "eyebrow h-9 px-4 font-medium";
const bodyRow = "transition-colors hover:bg-accent/60";
const bodyCell = "h-12 px-4 py-2 align-middle";
export function DataTable<T>({ caption, columns, rows, rowKey, empty }: { caption: string; columns: Array<{ key: string; header: string; render: (row: T) => React.ReactNode }>; rows: T[]; rowKey: (row: T) => string; empty?: React.ReactNode }) { return <div className={tableShell}><table className="w-full caption-bottom text-sm"><caption className="sr-only">{caption}</caption><thead className={tableHead}><tr>{columns.map((column) => <th className={headCell} scope="col" key={column.key}>{column.header}</th>)}</tr></thead><tbody className="divide-y divide-border">{rows.length ? rows.map((row) => <tr className={bodyRow} key={rowKey(row)}>{columns.map((column) => <td className={bodyCell} key={column.key}>{column.render(row)}</td>)}</tr>) : <tr><td colSpan={columns.length} className="p-4">{empty ?? <EmptyState title="No results" description="There are no records to show." />}</td></tr>}</tbody></table></div>; }
export function TanStackDataTable<T>({ caption, columns, rows, rowKey, empty }: { caption: string; columns: Array<{ key: string; header: string; render: (row: T) => React.ReactNode }>; rows: T[]; rowKey: (row: T) => string; empty?: React.ReactNode }) { const tableColumns = React.useMemo<ColumnDef<T>[]>(() => columns.map((column) => ({ id: column.key, header: column.header, cell: ({ row }) => column.render(row.original) })), [columns]); const table = useReactTable({ data: rows, columns: tableColumns, getCoreRowModel: getCoreRowModel() }); return <div className={tableShell}><table className="w-full caption-bottom text-sm"><caption className="sr-only">{caption}</caption><thead className={tableHead}>{table.getHeaderGroups().map((headerGroup) => <tr key={headerGroup.id}>{headerGroup.headers.map((header) => <th className={headCell} scope="col" key={header.id}>{flexRender(header.column.columnDef.header, header.getContext())}</th>)}</tr>)}</thead><tbody className="divide-y divide-border">{rows.length ? table.getRowModel().rows.map((row) => <tr className={bodyRow} key={rowKey(row.original)}>{row.getVisibleCells().map((cell) => <td className={bodyCell} key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>)}</tr>) : <tr><td colSpan={columns.length} className="p-4">{empty ?? <EmptyState title="No results" description="There are no records to show." />}</td></tr>}</tbody></table></div>; }

export function Pagination({ page, onPrevious, onNext }: { page: { previousCursor: string | null; nextCursor: string | null }; onPrevious: () => void; onNext: () => void }) {
  if (!page.previousCursor && !page.nextCursor) return null;
  return <nav aria-label="Pagination" className="flex justify-end pt-2"><div className="flex gap-2"><Button variant="secondary" size="sm" disabled={!page.previousCursor} onClick={onPrevious}>Previous</Button><Button variant="secondary" size="sm" disabled={!page.nextCursor} onClick={onNext}>Next</Button></div></nav>;
}

export interface NavigationItem { label: string; href: string; section: string; icon?: React.ComponentType<{ className?: string; "aria-hidden"?: boolean }>; match?: readonly string[] }
interface SidebarProps {
  items: readonly NavigationItem[];
  currentPath: string;
  linkComponent?: NavigationLink;
  footer?: React.ReactNode;
}

export function isNavigationActive(item: Pick<NavigationItem, "href" | "match">, currentPath: string) {
  const prefixes = [item.href, ...(item.match ?? [])];
  return prefixes.some((href) => currentPath === href || (href !== "/" && currentPath.startsWith(`${href}/`)));
}

function SidebarLinks({ items, currentPath, linkComponent: LinkComponent = AnchorLink, mobile = false }: SidebarProps & { mobile?: boolean }) {
  const sections = [...new Set(items.map((item) => item.section))];
  return <nav aria-label="Primary navigation" className="space-y-6">{sections.map((section) => <div key={section || "main"}>
    {section && <p className="eyebrow mb-1.5 px-3 text-sidebar-foreground/45">{section}</p>}
    <div className="space-y-0.5">{items.filter((item) => item.section === section).map((item) => {
      const active = isNavigationActive(item, currentPath);
      const Icon = item.icon;
      const link = <LinkComponent aria-current={active ? "page" : undefined} className={cn("group relative flex min-h-9 items-center gap-3 rounded-md px-3 py-1.5 text-sm transition-colors", active ? "bg-sidebar-accent font-medium text-sidebar-foreground" : "text-sidebar-foreground/65 hover:bg-sidebar-accent/60 hover:text-sidebar-foreground")} to={item.href}>
        {active && <span aria-hidden="true" className="absolute -left-3 top-1.5 bottom-1.5 w-[3px] rounded-r bg-primary" />}
        {Icon && <Icon aria-hidden className={cn("h-4 w-4 shrink-0", active ? "text-primary" : "opacity-70 group-hover:opacity-100")} />}
        {item.label}
      </LinkComponent>;
      return mobile ? <Dialog.Close asChild key={item.href}>{link}</Dialog.Close> : <React.Fragment key={item.href}>{link}</React.Fragment>;
    })}</div>
  </div>)}</nav>;
}

export function BrandMark({ className }: { className?: string }) {
  return <span className={cn("inline-flex items-center gap-2.5 font-semibold tracking-[-0.01em]", className)}>
    <span aria-hidden="true" className="relative grid h-6 w-6 place-items-center rounded-[5px] bg-sidebar-foreground text-sidebar"><span className="h-2 w-2 rounded-full bg-primary shadow-[0_0_8px_hsl(var(--primary))]" /></span>
    <span>Cuelight</span>
  </span>;
}

export function Sidebar({ footer, linkComponent: LinkComponent = AnchorLink, ...props }: SidebarProps) {
  return <aside className="hidden w-[var(--sidebar-width)] shrink-0 border-r border-sidebar-border bg-sidebar text-sidebar-foreground lg:block">
    <div className="sticky top-0 flex h-dvh flex-col">
      <div className="flex h-14 items-center px-5"><LinkComponent to="/" className="rounded-md"><BrandMark /></LinkComponent></div>
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-6 pt-4"><SidebarLinks {...props} linkComponent={LinkComponent} /></div>
      {footer && <div className="space-y-3 border-t border-sidebar-border p-4">{footer}</div>}
    </div>
  </aside>;
}

export function MobileSidebar({ footer, ...props }: SidebarProps) {
  return <div className="space-y-6"><SidebarLinks {...props} mobile />{footer && <div className="space-y-3 border-t border-sidebar-border pt-5">{footer}</div>}</div>;
}

export interface ModalProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: React.ReactNode;
  description?: React.ReactNode;
  children: React.ReactNode;
  footer?: React.ReactNode;
  className?: string;
}
/** Accessible modal dialog: focus trap, Esc to close, focus restoration. Full-height sheet on narrow screens. */
export function Modal({ open, onOpenChange, title, description, children, footer, className }: ModalProps) {
  return <Dialog.Root open={open} onOpenChange={onOpenChange}><Dialog.Portal><Dialog.Overlay className="fixed inset-0 z-40 bg-overlay backdrop-blur-[2px]" /><Dialog.Content {...(description ? {} : { "aria-describedby": undefined })} className={cn("fixed inset-x-0 bottom-0 top-[4vh] z-50 flex flex-col overflow-hidden rounded-t-xl border border-border bg-card text-card-foreground shadow-xl sm:inset-auto sm:left-1/2 sm:top-[8vh] sm:max-h-[84vh] sm:w-[min(56rem,calc(100vw-2rem))] sm:-translate-x-1/2 sm:rounded-xl", className)}>
    <div className="flex items-start justify-between gap-3 border-b border-border px-5 py-4"><div className="min-w-0"><Dialog.Title className="text-base font-semibold tracking-tight">{title}</Dialog.Title>{description && <Dialog.Description className="mt-0.5 text-sm text-muted-foreground">{description}</Dialog.Description>}</div><Dialog.Close asChild><Button variant="ghost" size="icon" aria-label="Close"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" className="h-4 w-4" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18" /></svg></Button></Dialog.Close></div>
    <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">{children}</div>
    {footer && <div className="flex flex-wrap items-center justify-end gap-2 border-t border-border px-5 py-3">{footer}</div>}
  </Dialog.Content></Dialog.Portal></Dialog.Root>;
}

export function MobileNav({ children }: { children: React.ReactNode }) { return <Dialog.Root><Dialog.Trigger asChild><Button variant="ghost" size="icon" className="-ml-2 lg:hidden" aria-label="Menu"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true"><path d="M4 7h16M4 12h16M4 17h10" /></svg></Button></Dialog.Trigger><Dialog.Portal><Dialog.Overlay className="fixed inset-0 z-40 bg-overlay backdrop-blur-[2px]" /><Dialog.Content aria-describedby={undefined} className="fixed inset-y-0 left-0 z-50 w-[min(18rem,86vw)] overflow-y-auto border-r border-sidebar-border bg-sidebar p-4 text-sidebar-foreground shadow-xl"><div className="mb-6 flex items-center justify-between"><Dialog.Title><BrandMark /></Dialog.Title><Dialog.Close asChild><Button variant="ghost" size="sm">Close</Button></Dialog.Close></div>{children}</Dialog.Content></Dialog.Portal></Dialog.Root>; }

export interface CommandItem { id: string; label: string; group: string; href: string; hint?: string }
export function CommandPalette({ open, onOpenChange, items, onSelect, loading = false }: { open: boolean; onOpenChange: (open: boolean) => void; items: readonly CommandItem[]; onSelect: (item: CommandItem) => void; loading?: boolean }) {
  const [query, setQuery] = React.useState("");
  const [cursor, setCursor] = React.useState(0);
  React.useEffect(() => { if (!open) { setQuery(""); setCursor(0); } }, [open]);
  const needle = query.trim().toLocaleLowerCase();
  const matches = (needle ? items.filter((item) => `${item.label} ${item.hint ?? ""} ${item.group}`.toLocaleLowerCase().includes(needle)) : items).slice(0, 40);
  const groups = [...new Set(matches.map((item) => item.group))];
  const active = matches[Math.min(cursor, Math.max(matches.length - 1, 0))];
  const choose = (item: CommandItem | undefined) => { if (!item) return; onOpenChange(false); onSelect(item); };
  return <Dialog.Root open={open} onOpenChange={onOpenChange}><Dialog.Portal><Dialog.Overlay className="fixed inset-0 z-40 bg-overlay backdrop-blur-[2px]" /><Dialog.Content aria-describedby={undefined} className="fixed left-1/2 top-[12vh] z-50 w-[min(36rem,calc(100vw-2rem))] -translate-x-1/2 overflow-hidden rounded-xl border border-border bg-card text-card-foreground shadow-xl">
    <Dialog.Title className="sr-only">Jump to</Dialog.Title>
    <div className="flex items-center gap-3 border-b border-border px-4"><span aria-hidden="true" className="eyebrow text-primary">Go</span><input autoFocus aria-label="Search destinations" role="combobox" aria-expanded="true" aria-controls="command-results" aria-activedescendant={active ? `command-${active.id}` : undefined} className="h-12 min-w-0 flex-1 border-0 bg-transparent text-base outline-none focus-visible:shadow-none" placeholder="Screens, media, deployments, pages…" value={query} onChange={(event) => { setQuery(event.currentTarget.value); setCursor(0); }} onKeyDown={(event) => {
      if (event.key === "ArrowDown") { event.preventDefault(); setCursor((value) => Math.min(value + 1, matches.length - 1)); }
      if (event.key === "ArrowUp") { event.preventDefault(); setCursor((value) => Math.max(value - 1, 0)); }
      if (event.key === "Enter") { event.preventDefault(); choose(active); }
    }} /><kbd className="hidden rounded border border-border px-1.5 font-mono text-[11px] text-muted-foreground sm:inline">esc</kbd></div>
    <div id="command-results" role="listbox" aria-label="Destinations" className="max-h-[60vh] overflow-y-auto p-2">
      {groups.map((group) => <div key={group} role="group" aria-label={group} className="pb-2"><p className="eyebrow px-2 py-1.5">{group}</p>{matches.filter((item) => item.group === group).map((item) => <div key={item.id} id={`command-${item.id}`} role="option" aria-selected={item === active} className={cn("flex cursor-pointer items-center justify-between gap-3 rounded-md px-2 py-2 text-sm", item === active ? "bg-accent text-foreground" : "text-foreground/85")} onMouseEnter={() => setCursor(matches.indexOf(item))} onClick={() => choose(item)}><span className="truncate">{item.label}</span>{item.hint && <span className="shrink-0 truncate font-mono text-[11px] text-muted-foreground">{item.hint}</span>}</div>)}</div>)}
      {!matches.length && <p className="px-2 py-6 text-center text-sm text-muted-foreground">{loading ? "Loading…" : "Nothing matches."}</p>}
    </div>
  </Dialog.Content></Dialog.Portal></Dialog.Root>;
}

export interface PixelDimensions {
  width: number;
  height: number;
}

function formatPixels(dimensions: PixelDimensions): string | null {
  if (!Number.isFinite(dimensions.width) || !Number.isFinite(dimensions.height) || dimensions.width <= 0 || dimensions.height <= 0) return null;
  return `${Math.round(dimensions.width).toLocaleString()} × ${Math.round(dimensions.height).toLocaleString()} px`;
}

export function humanizeStatus(status: string): string {
  const words = status
    .trim()
    .replace(/([a-z\d])([A-Z])/g, "$1 $2")
    .replace(/[_-]+/g, " ")
    .replace(/\s+/g, " ")
    .toLocaleLowerCase();
  return words ? `${words.charAt(0).toLocaleUpperCase()}${words.slice(1)}` : "Unknown";
}

function inferStatusTone(status: string): StatusTone {
  const normalized = humanizeStatus(status).toLocaleLowerCase();
  if (/\b(error|errored|failed|failure|offline|disconnected|unhealthy|invalid|blocked)\b/.test(normalized)) return "danger";
  if (/\b(warning|degraded|stale|pending|buffering|retrying|paused|not ready)\b/.test(normalized)) return "warning";
  if (/\b(online|playing|ready|healthy|complete|completed|active|connected|synced)\b/.test(normalized)) return "success";
  if (/\b(loading|starting|processing|syncing)\b/.test(normalized)) return "info";
  return "neutral";
}

const statusDotStyles: Record<StatusTone, string> = {
  success: "bg-status-success",
  warning: "bg-status-warning",
  danger: "bg-status-danger",
  neutral: "bg-muted-foreground",
  info: "bg-status-info",
};

export interface StatusSummaryProps {
  status: string;
  label?: string;
  detail?: React.ReactNode;
  tone?: StatusTone;
  className?: string;
  ariaLive?: "off" | "polite" | "assertive";
}

export function StatusSummary({ status, label, detail, tone = inferStatusTone(status), className, ariaLive = "off" }: StatusSummaryProps) {
  return <span className={cn("inline-flex min-w-0 items-center gap-2 text-sm", className)} aria-live={ariaLive}>
    <span aria-hidden="true" className={cn("h-2 w-2 shrink-0 rounded-full", statusDotStyles[tone])} />
    <span className="font-medium text-foreground">{label ?? humanizeStatus(status)}</span>
    {detail && <span className="truncate text-muted-foreground">{detail}</span>}
  </span>;
}

export type PreviewMedia =
  | {
      kind: "image";
      src: string;
      alt: string;
      srcSet?: string;
      sizes?: string;
      loading?: "eager" | "lazy";
      onLoad?: () => void;
      onError?: (event: React.SyntheticEvent<HTMLImageElement>) => void;
    }
  | {
      kind: "video";
      src: string;
      label: string;
      poster?: string;
      muted?: boolean;
      loop?: boolean;
      autoPlay?: boolean;
      controls?: boolean;
      onLoad?: () => void;
      onError?: (event: React.SyntheticEvent<HTMLVideoElement>) => void;
    };

export interface PreviewSurfaceProps {
  media?: PreviewMedia;
  state?: "ready" | "loading" | "empty" | "error";
  aspectRatio?: React.CSSProperties["aspectRatio"];
  fit?: "contain" | "cover";
  className?: string;
  style?: React.CSSProperties;
  id?: string;
  "aria-label"?: string;
  loadingLabel?: string;
  emptyTitle?: string;
  emptyDescription?: string;
  errorTitle?: string;
  errorDescription?: string;
  onRetry?: () => void;
  overlay?: React.ReactNode;
}

type ObservedPreviewState = { source: string; state: "loading" | "ready" | "error" };

export function PreviewSurface({
  media,
  state: requestedState,
  aspectRatio = "16 / 9",
  fit = "contain",
  className,
  style,
  id,
  "aria-label": ariaLabel,
  loadingLabel = "Loading preview…",
  emptyTitle = "No preview",
  emptyDescription = "A preview is not available for this item.",
  errorTitle = "Preview unavailable",
  errorDescription = "The preview could not be loaded.",
  onRetry,
  overlay,
}: PreviewSurfaceProps) {
  const source = media ? `${media.kind}:${media.src}` : "";
  const [observed, setObserved] = React.useState<ObservedPreviewState>({ source, state: media ? "loading" : "ready" });
  const [attempt, setAttempt] = React.useState(0);
  const observedState = observed.source === source ? observed.state : "loading";
  const state = requestedState && requestedState !== "ready" ? requestedState : media ? observedState : "empty";
  const fitClass = fit === "cover" ? "object-cover" : "object-contain";
  const shouldLoadMedia = Boolean(media) && (requestedState === undefined || requestedState === "ready");

  let mediaContent: React.ReactNode;
  if (shouldLoadMedia && media?.kind === "image") {
    mediaContent = <img
      key={`${source}:${attempt}`}
      src={media.src}
      srcSet={media.srcSet}
      sizes={media.sizes}
      alt={media.alt}
      loading={media.loading ?? "lazy"}
      decoding="async"
      className={cn("absolute inset-0 h-full w-full", fitClass)}
      onLoad={() => {
        setObserved({ source, state: "ready" });
        media.onLoad?.();
      }}
      onError={(event) => {
        setObserved({ source, state: "error" });
        media.onError?.(event);
      }}
    />;
  } else if (shouldLoadMedia && media?.kind === "video") {
    mediaContent = <video
      key={`${source}:${attempt}`}
      src={media.src}
      aria-label={media.label}
      poster={media.poster}
      controls={media.controls ?? true}
      muted={media.autoPlay ? true : media.muted}
      loop={media.loop}
      autoPlay={media.autoPlay}
      playsInline
      preload="metadata"
      className={cn("absolute inset-0 h-full w-full", fitClass)}
      onLoadedData={() => {
        setObserved({ source, state: "ready" });
        media.onLoad?.();
      }}
      onError={(event) => {
        setObserved({ source, state: "error" });
        media.onError?.(event);
      }}
    />;
  }

  let stateContent: React.ReactNode;
  if (state === "loading") {
    stateContent = <div role="status" className="absolute inset-0 flex items-center justify-center bg-muted p-6 text-center text-sm text-muted-foreground">
      <span className="animate-pulse">{loadingLabel}</span>
    </div>;
  } else if (state === "error") {
    stateContent = <div role="alert" className="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-muted p-6 text-center">
      <p className="text-sm font-medium text-foreground">{errorTitle}</p>
      <p className="max-w-md text-xs text-muted-foreground">{errorDescription}</p>
      {onRetry && <Button type="button" variant="secondary" size="sm" onClick={() => {
        setObserved({ source, state: "loading" });
        setAttempt((current) => current + 1);
        onRetry();
      }}>Try again</Button>}
    </div>;
  } else if (state === "empty") {
    stateContent = <div className="absolute inset-0 flex flex-col items-center justify-center gap-1 bg-muted/50 p-6 text-center">
      <p className="text-sm font-medium text-foreground">{emptyTitle}</p>
      <p className="max-w-md text-xs text-muted-foreground">{emptyDescription}</p>
    </div>;
  }

  return <div
    id={id}
    role={ariaLabel ? "group" : undefined}
    aria-label={ariaLabel}
    aria-busy={state === "loading" || undefined}
    className={cn("relative isolate w-full overflow-hidden rounded-md border border-border bg-muted/50", className)}
    style={{ ...style, aspectRatio }}
  >
    {mediaContent}
    {stateContent}
    {state === "ready" && overlay && <div className="pointer-events-none absolute inset-x-0 bottom-0 bg-card/95 p-3 text-sm text-card-foreground">{overlay}</div>}
  </div>;
}

const mosaicColumns = {
  2: "sm:grid-cols-2",
  3: "sm:grid-cols-2 lg:grid-cols-3",
  4: "sm:grid-cols-2 lg:grid-cols-4",
} as const;

export interface PreviewMosaicProps extends Omit<React.HTMLAttributes<HTMLDivElement>, "children"> {
  children: React.ReactNode;
  label: string;
  columns?: keyof typeof mosaicColumns;
}

export function PreviewMosaic({ children, label, columns = 3, className, ...props }: PreviewMosaicProps) {
  return <div role="list" aria-label={label} className={cn("grid grid-cols-1 gap-3", mosaicColumns[columns], className)} {...props}>
    {React.Children.map(children, (child) => <div role="listitem" className="min-w-0">{child}</div>)}
  </div>;
}

export interface MediaCardProps extends Omit<React.HTMLAttributes<HTMLElement>, "title"> {
  title: string;
  description?: string;
  artist?: string;
  preview: React.ReactNode;
  dimensions?: PixelDimensions;
  metadata?: React.ReactNode;
  status?: string;
  statusLabel?: string;
  statusTone?: StatusTone;
  titleHref?: string;
  linkComponent?: NavigationLink;
  actions?: React.ReactNode;
}

export function MediaCard({
  title,
  description,
  artist,
  preview,
  dimensions,
  metadata,
  status,
  statusLabel,
  statusTone,
  titleHref,
  linkComponent: LinkComponent = AnchorLink,
  actions,
  className,
  ...props
}: MediaCardProps) {
  const pixels = dimensions ? formatPixels(dimensions) : null;
  return <article className={cn("media-card relative min-w-0 overflow-hidden rounded-xl border border-border bg-card text-card-foreground transition-colors", titleHref && "media-card-linked hover:border-input focus-within:border-ring", className)} {...props}>
    {preview}
    <div className="space-y-2 p-4">
      <div className="min-w-0">
        <h3 className="truncate text-sm font-semibold">
          {titleHref ? <LinkComponent className="media-card-link rounded-sm hover:text-primary" to={titleHref}>{title}</LinkComponent> : title}
        </h3>
        {artist && <p className="mt-1 truncate text-sm text-muted-foreground">{artist}</p>}
        {description && <p className="mt-2 line-clamp-2 text-sm leading-relaxed text-muted-foreground">{description}</p>}
      </div>
      {(pixels || metadata || status) && <div className="flex flex-wrap items-center gap-x-3 gap-y-2 text-xs text-muted-foreground">
        {pixels && <span className="tabular-nums">{pixels}</span>}
        {metadata}
        {status && <StatusSummary className="text-xs" status={status} label={statusLabel} tone={statusTone} />}
      </div>}
      {actions && <div className="relative z-20 flex flex-wrap items-center gap-2 border-t border-border pt-3" aria-label={`${title} actions`}>{actions}</div>}
    </div>
  </article>;
}

function formatPlaybackTime(milliseconds: number): string {
  if (!Number.isFinite(milliseconds) || milliseconds < 0) return "0:00";
  const totalSeconds = Math.floor(milliseconds / 1_000);
  const hours = Math.floor(totalSeconds / 3_600);
  const minutes = Math.floor((totalSeconds % 3_600) / 60);
  const seconds = totalSeconds % 60;
  return hours ? `${hours}:${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}` : `${minutes}:${String(seconds).padStart(2, "0")}`;
}

export interface NowPlayingCardProps extends Omit<React.HTMLAttributes<HTMLElement>, "title"> {
  title: string;
  subtitle?: string;
  preview?: React.ReactNode;
  status: string;
  statusLabel?: string;
  statusTone?: StatusTone;
  dimensions?: PixelDimensions;
  positionMs?: number;
  durationMs?: number;
  startedAt?: string;
  details?: React.ReactNode;
  actions?: React.ReactNode;
}

export function NowPlayingCard({
  title,
  subtitle,
  preview,
  status,
  statusLabel,
  statusTone,
  dimensions,
  positionMs,
  durationMs,
  startedAt,
  details,
  actions,
  className,
  ...props
}: NowPlayingCardProps) {
  const hasProgress = Number.isFinite(positionMs) && Number.isFinite(durationMs) && (durationMs ?? 0) > 0;
  const progress = hasProgress ? Math.min(Math.max(positionMs ?? 0, 0), durationMs ?? 0) : 0;
  const pixels = dimensions ? formatPixels(dimensions) : null;
  const started = startedAt ? new Date(startedAt) : null;
  const startedLabel = started && !Number.isNaN(started.valueOf())
    ? started.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })
    : null;

  return <article className={cn("rounded-lg border border-border bg-card p-4 text-card-foreground shadow-sm", className)} {...props}>
    <div className={cn("grid gap-4", preview && "sm:grid-cols-3")}>
      {preview && <div className="min-w-0">{preview}</div>}
      <div className={cn("min-w-0 space-y-3", preview && "sm:col-span-2")}>
        <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
          <div className="min-w-0">
            <p className="text-xs font-semibold uppercase tracking-wide text-primary">Now playing</p>
            <h2 className="mt-1 truncate text-xl font-semibold tracking-tight">{title}</h2>
            {subtitle && <p className="mt-1 truncate text-sm text-muted-foreground">{subtitle}</p>}
          </div>
          <StatusSummary className="shrink-0" status={status} label={statusLabel} tone={statusTone} ariaLive="polite" />
        </div>
        {(pixels || startedLabel) && <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
          {pixels && <span className="tabular-nums">{pixels}</span>}
          {startedLabel && <time dateTime={startedAt}>Started {startedLabel}</time>}
        </div>}
        {hasProgress && <div className="space-y-1">
          <progress className="h-2 w-full accent-primary" max={durationMs} value={progress} aria-label="Playback position" />
          <div className="flex justify-between text-xs tabular-nums text-muted-foreground">
            <span>{formatPlaybackTime(progress)}</span>
            <span>{formatPlaybackTime(durationMs ?? 0)}</span>
          </div>
        </div>}
        {details}
        {actions && <div className="flex flex-wrap items-center gap-2 border-t border-border pt-3" aria-label={`${title} actions`}>{actions}</div>}
      </div>
    </div>
  </article>;
}

export interface TechnicalDetail {
  label: React.ReactNode;
  value: React.ReactNode;
}

export interface TechnicalDetailsProps extends React.DetailsHTMLAttributes<HTMLDetailsElement> {
  summary?: React.ReactNode;
  items?: readonly TechnicalDetail[];
}

export function TechnicalDetails({ summary = "Technical details", items, children, className, ...props }: TechnicalDetailsProps) {
  return <details className={cn("group rounded-md border border-border bg-muted/30 text-sm", className)} {...props}>
    <summary className="cursor-pointer select-none rounded-md px-4 py-3 font-medium text-foreground marker:text-muted-foreground hover:bg-muted/60">
      {summary}
    </summary>
    <div className="border-t border-border px-4 py-3">
      {items?.length ? <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-2">
        {items.map((item, index) => <div className="min-w-0" key={index}>
          <dt className="text-xs text-muted-foreground">{item.label}</dt>
          <dd className="mt-1 break-words text-foreground">{item.value}</dd>
        </div>)}
      </dl> : null}
      {children && <div className={cn(items?.length && "mt-4")}>{children}</div>}
    </div>
  </details>;
}
