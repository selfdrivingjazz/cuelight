import * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import * as Dialog from "@radix-ui/react-dialog";
import { cva, type VariantProps } from "class-variance-authority";
import { flexRender, getCoreRowModel, useReactTable, type ColumnDef } from "@tanstack/react-table";
import { cn } from "./utils";

const buttonStyles = cva("inline-flex items-center justify-center rounded-md text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50", {
  variants: { variant: { primary: "bg-primary text-primary-foreground hover:bg-primary/90", secondary: "border border-border bg-background hover:bg-accent", ghost: "hover:bg-accent hover:text-accent-foreground" }, size: { sm: "h-8 px-3", md: "h-9 px-4", lg: "h-10 px-5" } },
  defaultVariants: { variant: "primary", size: "md" },
});

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement>, VariantProps<typeof buttonStyles> { asChild?: boolean }
export function Button({ className, variant, size, asChild = false, ...props }: ButtonProps) {
  const Comp = asChild ? Slot : "button";
  return <Comp className={cn(buttonStyles({ variant, size }), className)} {...props} />;
}

export function Card({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) { return <section className={cn("rounded-lg border border-border bg-card text-card-foreground shadow-sm", className)} {...props} />; }
export function CardHeader({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) { return <div className={cn("flex flex-col gap-1.5 p-5", className)} {...props} />; }
export function CardTitle({ className, ...props }: React.HTMLAttributes<HTMLHeadingElement>) { return <h2 className={cn("text-base font-semibold tracking-tight", className)} {...props} />; }
export function CardContent({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) { return <div className={cn("p-5 pt-0", className)} {...props} />; }

export type StatusTone = "success" | "warning" | "danger" | "neutral" | "info";
const statusStyles: Record<StatusTone, string> = { success: "bg-status-success/15 text-status-success", warning: "bg-status-warning/15 text-status-warning", danger: "bg-status-danger/15 text-status-danger", neutral: "bg-muted text-muted-foreground", info: "bg-status-info/15 text-status-info" };
export function StatusBadge({ tone, children, className }: { tone: StatusTone; children: React.ReactNode; className?: string }) { return <span className={cn("inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium", statusStyles[tone], className)}><span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-current" />{children}</span>; }

export function Skeleton({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) { return <div role="status" aria-label="Loading" className={cn("animate-pulse rounded-md bg-muted", className)} {...props} />; }
export function EmptyState({ title, description, action }: { title: string; description: string; action?: React.ReactNode }) { return <div className="flex min-h-40 flex-col items-center justify-center gap-2 rounded-lg border border-dashed border-border p-8 text-center"><h3 className="font-medium">{title}</h3><p className="max-w-md text-sm text-muted-foreground">{description}</p>{action}</div>; }
export function ErrorState({ title = "Something went wrong", description, onRetry }: { title?: string; description: string; onRetry?: () => void }) { return <div role="alert" className="rounded-lg border border-status-danger/40 bg-status-danger/5 p-5"><h3 className="font-medium text-status-danger">{title}</h3><p className="mt-1 text-sm text-muted-foreground">{description}</p>{onRetry && <Button className="mt-3" variant="secondary" size="sm" onClick={onRetry}>Try again</Button>}</div>; }

export type NavigationLink = React.ComponentType<{ to: string; className?: string; children?: React.ReactNode; "aria-current"?: "page" | undefined }>;
interface NavigationProps { linkComponent?: NavigationLink }
const AnchorLink: NavigationLink = ({ to, ...props }) => <a href={to} {...props} />;
export function Breadcrumbs({ items, linkComponent: LinkComponent = AnchorLink }: { items: Array<{ label: string; href?: string }>; linkComponent?: NavigationLink }) { return <nav aria-label="Breadcrumb" className="text-sm text-muted-foreground"><ol className="flex flex-wrap items-center gap-2">{items.map((item, index) => <li key={`${item.label}-${index}`} className="flex items-center gap-2">{index > 0 && <span aria-hidden="true">/</span>}{item.href ? <LinkComponent className="hover:text-foreground hover:underline" to={item.href}>{item.label}</LinkComponent> : <span aria-current="page" className="text-foreground">{item.label}</span>}</li>)}</ol></nav>; }

export function DataTable<T>({ caption, columns, rows, rowKey, empty }: { caption: string; columns: Array<{ key: string; header: string; render: (row: T) => React.ReactNode }>; rows: T[]; rowKey: (row: T) => string; empty?: React.ReactNode }) { return <div className="overflow-x-auto rounded-lg border border-border"><table className="w-full caption-bottom text-sm"><caption className="sr-only">{caption}</caption><thead className="border-b border-border bg-muted/50 text-left text-xs uppercase tracking-wide text-muted-foreground"><tr>{columns.map((column) => <th className="h-10 px-4 font-medium" scope="col" key={column.key}>{column.header}</th>)}</tr></thead><tbody className="divide-y divide-border">{rows.length ? rows.map((row) => <tr className="h-12 transition-colors hover:bg-muted/40" key={rowKey(row)}>{columns.map((column) => <td className="px-4" key={column.key}>{column.render(row)}</td>)}</tr>) : <tr><td colSpan={columns.length} className="p-8">{empty ?? <EmptyState title="No results" description="There are no records to show." />}</td></tr>}</tbody></table></div>; }
export function TanStackDataTable<T>({ caption, columns, rows, rowKey, empty }: { caption: string; columns: Array<{ key: string; header: string; render: (row: T) => React.ReactNode }>; rows: T[]; rowKey: (row: T) => string; empty?: React.ReactNode }) { const tableColumns = React.useMemo<ColumnDef<T>[]>(() => columns.map((column) => ({ id: column.key, header: column.header, cell: ({ row }) => column.render(row.original) })), [columns]); const table = useReactTable({ data: rows, columns: tableColumns, getCoreRowModel: getCoreRowModel() }); return <div className="overflow-x-auto rounded-lg border border-border"><table className="w-full caption-bottom text-sm"><caption className="sr-only">{caption}</caption><thead className="border-b border-border bg-muted/50 text-left text-xs uppercase tracking-wide text-muted-foreground">{table.getHeaderGroups().map((headerGroup) => <tr key={headerGroup.id}>{headerGroup.headers.map((header) => <th className="h-10 px-4 font-medium" scope="col" key={header.id}>{flexRender(header.column.columnDef.header, header.getContext())}</th>)}</tr>)}</thead><tbody className="divide-y divide-border">{rows.length ? table.getRowModel().rows.map((row) => <tr className="h-12 transition-colors hover:bg-muted/40" key={rowKey(row.original)}>{row.getVisibleCells().map((cell) => <td className="px-4" key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>)}</tr>) : <tr><td colSpan={columns.length} className="p-8">{empty ?? <EmptyState title="No results" description="There are no records to show." />}</td></tr>}</tbody></table></div>; }

export function Pagination({ page, onPrevious, onNext }: { page: { previousCursor: string | null; nextCursor: string | null }; onPrevious: () => void; onNext: () => void }) { return <nav aria-label="Pagination" className="flex justify-end"><div className="flex gap-2"><Button variant="secondary" size="sm" disabled={!page.previousCursor} onClick={onPrevious}>Previous</Button><Button variant="secondary" size="sm" disabled={!page.nextCursor} onClick={onNext}>Next</Button></div></nav>; }

export function Sidebar({ items, currentPath, linkComponent: LinkComponent = AnchorLink }: { items: Array<{ label: string; href: string; section: string }>; currentPath: string; linkComponent?: NavigationLink }) {
  const sections = [...new Set(items.map((item) => item.section))];
  return <aside className="hidden w-[var(--sidebar-width)] shrink-0 border-r border-border bg-sidebar md:block" aria-label="Primary navigation"><div className="sticky top-0 flex h-screen flex-col"><div className="flex h-16 items-center border-b border-border px-5"><span className="text-lg font-semibold tracking-tight">Cable <span className="text-primary">Control</span></span></div><nav className="flex-1 space-y-6 overflow-y-auto p-3">{sections.map((section) => <div key={section}>{section && <p className="mb-2 px-3 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">{section}</p>}<div className="space-y-0.5">{items.filter((item) => item.section === section).map((item) => <LinkComponent key={item.href} aria-current={currentPath === item.href ? "page" : undefined} className={cn("block rounded-md px-3 py-2 text-sm transition-colors", currentPath === item.href ? "bg-primary/10 font-medium text-primary" : "text-muted-foreground hover:bg-accent hover:text-foreground")} to={item.href}>{item.label}</LinkComponent>)}</div></div>)}</nav></div></aside>;
}
export function MobileSidebar({ items, currentPath, linkComponent: LinkComponent = AnchorLink }: { items: Array<{ label: string; href: string; section: string }>; currentPath: string; linkComponent?: NavigationLink }) {
  const sections = [...new Set(items.map((item) => item.section))];
  return <nav aria-label="Primary navigation" className="space-y-6">{sections.map((section) => <div key={section}>{section && <p className="mb-2 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">{section}</p>}{items.filter((item) => item.section === section).map((item) => <LinkComponent key={item.href} aria-current={currentPath === item.href ? "page" : undefined} className={cn("block rounded-md px-3 py-2 text-sm", currentPath === item.href ? "bg-primary/10 font-medium text-primary" : "text-muted-foreground hover:bg-accent hover:text-foreground")} to={item.href}>{item.label}</LinkComponent>)}</div>)}</nav>;
}

export function MobileNav({ children }: { children: React.ReactNode }) { return <Dialog.Root><Dialog.Trigger asChild><Button variant="secondary" size="sm" className="md:hidden">Menu</Button></Dialog.Trigger><Dialog.Portal><Dialog.Overlay className="fixed inset-0 z-40 bg-overlay" /><Dialog.Content className="fixed inset-y-0 left-0 z-50 w-72 bg-sidebar p-5 text-sidebar-foreground shadow-xl"><Dialog.Title className="sr-only">Navigation</Dialog.Title>{children}<Dialog.Close asChild><Button variant="secondary" className="mt-6 w-full">Close</Button></Dialog.Close></Dialog.Content></Dialog.Portal></Dialog.Root>; }

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
  return <article className={cn("overflow-hidden rounded-lg border border-border bg-card text-card-foreground shadow-sm", className)} {...props}>
    {preview}
    <div className="space-y-3 p-4">
      <div className="min-w-0">
        <h3 className="truncate text-base font-semibold tracking-tight">
          {titleHref ? <LinkComponent className="rounded-sm hover:text-primary hover:underline" to={titleHref}>{title}</LinkComponent> : title}
        </h3>
        {artist && <p className="mt-1 truncate text-sm text-muted-foreground">{artist}</p>}
        {description && <p className="mt-2 text-sm text-muted-foreground">{description}</p>}
      </div>
      {(pixels || metadata || status) && <div className="flex flex-wrap items-center gap-x-3 gap-y-2 text-xs text-muted-foreground">
        {pixels && <span className="tabular-nums">{pixels}</span>}
        {metadata}
        {status && <StatusSummary className="text-xs" status={status} label={statusLabel} tone={statusTone} />}
      </div>}
      {actions && <div className="flex flex-wrap items-center gap-2 border-t border-border pt-3" aria-label={`${title} actions`}>{actions}</div>}
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
