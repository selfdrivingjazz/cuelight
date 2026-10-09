import * as React from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import type { components } from "@cable/api-client";
import { Button, EmptyState, ErrorState, Modal, Skeleton } from "@cable/controller-ui";
import { api, readApiResult } from "../api";
import { MediaThumbnail } from "../media-thumbnail";

type Media = components["schemas"]["Media"];
type MediaPage = components["schemas"]["MediaPage"];
export type MediaPickerKind = "all" | "image" | "video" | "audio" | "web";

const PAGE_SIZE = 24;

/** Toggles `media` in an ordered selection. Single-select replaces; multi-select appends or removes, preserving pick order. */
export function toggleMediaSelection(selection: readonly Media[], media: Media, multiple: boolean): Media[] {
  const present = selection.some((item) => item.id === media.id);
  if (!multiple) return present ? [] : [media];
  return present ? selection.filter((item) => item.id !== media.id) : [...selection, media];
}

/** Flattens fetched pages, dropping duplicates (cursor overlap) and media whose asset id is excluded. */
export function pickableMedia(pages: readonly MediaPage[], excludeAssetIds: ReadonlySet<string>): Media[] {
  const seen = new Set<string>();
  const out: Media[] = [];
  for (const page of pages) for (const media of page.items) {
    if ((media.assetId && excludeAssetIds.has(media.assetId)) || seen.has(media.id)) continue;
    seen.add(media.id);
    out.push(media);
  }
  return out;
}

export function mediaKindLabel(media: Media): string {
  if (media.kind === "web") return media.appAccess ? "Cuelight app" : "Web page";
  const type = media.artifact?.mediaType ?? "";
  return type.startsWith("image/") ? "Image" : type.startsWith("video/") ? "Video" : type.startsWith("audio/") ? "Audio" : type || "File";
}

export interface MediaPickerProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: (media: Media[]) => void;
  title?: string;
  description?: string;
  confirmLabel?: (count: number) => string;
  multiple?: boolean;
  /** Asset ids that cannot be chosen (e.g. already in the playlist). */
  excludeAssetIds?: readonly string[];
  /** Restrict selectable media kinds; when a single kind is given the type filter is hidden. */
  kinds?: readonly Exclude<MediaPickerKind, "all">[];
  /** Extra controls rendered in the footer next to the confirm button (e.g. default duration). */
  footerStart?: React.ReactNode;
  /** Extra predicate for choosable media (e.g. hide items already present). */
  filter?: (media: Media) => boolean;
}

/**
 * Modal media chooser backed by the paginated GET /media API. Search, type filter, thumbnails, multi-select,
 * infinite scroll. Keyboard: Tab through results, Space/Enter toggles, Ctrl/⌘+Enter confirms, Escape closes.
 */
export function MediaPicker({ open, onOpenChange, onConfirm, title = "Choose media", description, confirmLabel, multiple = true, excludeAssetIds = [], kinds, footerStart, filter }: MediaPickerProps) {
  const [search, setSearch] = React.useState("");
  const [debounced, setDebounced] = React.useState("");
  const [kind, setKind] = React.useState<MediaPickerKind>(kinds?.length === 1 ? kinds[0]! : "all");
  const [selection, setSelection] = React.useState<Media[]>([]);
  React.useEffect(() => { const timer = window.setTimeout(() => setDebounced(search.trim()), 250); return () => window.clearTimeout(timer); }, [search]);
  React.useEffect(() => { if (!open) { setSearch(""); setDebounced(""); setSelection([]); setKind(kinds?.length === 1 ? kinds[0]! : "all"); } }, [open, kinds]);

  const query = useInfiniteQuery({
    queryKey: ["controller", "media-picker", debounced, kind],
    enabled: open,
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page: MediaPage) => page.page.hasMore ? page.page.nextCursor ?? undefined : undefined,
    queryFn: async ({ pageParam }) => readApiResult(await api.GET("/media", { params: { query: { limit: PAGE_SIZE, playable: true, cursor: pageParam, search: debounced || undefined, kind: kind === "web" ? "web" : undefined, mediaType: kind === "all" || kind === "web" ? undefined : kind } } })),
  });
  const excluded = React.useMemo(() => new Set(excludeAssetIds), [excludeAssetIds]);
  const allowed = (media: Media) => (!filter || filter(media)) && (!kinds?.length || kinds.some((value) => value === "web" ? media.kind === "web" : media.kind !== "web" && Boolean(media.artifact?.mediaType.startsWith(`${value}/`))));
  const results = pickableMedia(query.data?.pages ?? [], excluded).filter(allowed);
  const selectedIds = new Set(selection.map((media) => media.id));

  const sentinel = React.useRef<HTMLDivElement | null>(null);
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = query;
  React.useEffect(() => {
    const node = sentinel.current;
    if (!node || !hasNextPage || typeof IntersectionObserver === "undefined") return;
    const observer = new IntersectionObserver((entries) => { if (entries.some((entry) => entry.isIntersecting) && !isFetchingNextPage) void fetchNextPage(); }, { rootMargin: "200px" });
    observer.observe(node);
    return () => observer.disconnect();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage, results.length]);

  const confirm = (chosen = selection) => { if (!chosen.length) return; onConfirm(chosen); onOpenChange(false); };
  const toggle = (media: Media) => {
    const next = toggleMediaSelection(selection, media, multiple);
    if (!multiple && next.length) { confirm(next); return; }
    setSelection(next);
  };
  const label = confirmLabel ?? ((count: number) => count ? `Add ${count} ${count === 1 ? "item" : "items"}` : "Add");
  const kindOptions: MediaPickerKind[] = kinds?.length ? (kinds.length > 1 ? ["all", ...kinds] : []) : ["all", "image", "video", "audio", "web"];

  return <Modal
    open={open}
    onOpenChange={onOpenChange}
    title={title}
    description={description ?? (multiple ? "Select one or more items. Ctrl+Enter adds the selection." : "Select an item.")}
    footer={<>
      <div className="mr-auto flex min-w-0 flex-wrap items-center gap-3 text-sm text-muted-foreground">{footerStart}{multiple && <span aria-live="polite">{selection.length} selected</span>}</div>
      <Button type="button" variant="secondary" onClick={() => onOpenChange(false)}>Cancel</Button>
      {multiple && <Button type="button" disabled={!selection.length} onClick={() => confirm()}>{label(selection.length)}</Button>}
    </>}
  >
    <div className="space-y-4" onKeyDown={(event) => { if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) { event.preventDefault(); confirm(); } }}>
      <div className="flex flex-col gap-2 sm:flex-row">
        <input
          type="search"
          autoFocus
          aria-label="Search media"
          placeholder="Search by title, artist, or tag"
          className="h-9 min-w-0 flex-1 rounded-md border border-input bg-background px-3 text-sm focus-visible:ring-2 focus-visible:ring-ring"
          value={search}
          onChange={(event) => setSearch(event.currentTarget.value)}
          onKeyDown={(event) => { if (event.key === "Enter" && !event.ctrlKey && !event.metaKey) { event.preventDefault(); if (multiple) confirm(); else if (results[0]) toggle(results[0]); } }}
        />
        {kindOptions.length > 0 && <div role="radiogroup" aria-label="Media type" className="inline-flex rounded-md border border-input p-0.5">
          {kindOptions.map((value) => <button key={value} type="button" role="radio" aria-checked={kind === value} onClick={() => setKind(value)} className={`h-8 flex-1 rounded px-3 text-[13px] font-medium capitalize ${kind === value ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:bg-accent hover:text-foreground"}`}>{value === "all" ? "All" : value === "web" ? "Web" : value}</button>)}
        </div>}
      </div>
      {query.isError ? <ErrorState title="Media unavailable" description={query.error instanceof Error ? query.error.message : "The media library could not be loaded."} onRetry={() => void query.refetch()} />
        : query.isPending ? <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">{Array.from({ length: 8 }, (_, index) => <Skeleton key={index} className="aspect-video" />)}</div>
        : results.length === 0 ? <EmptyState title={debounced || kind !== "all" ? "No matching media" : "No media available"} description={debounced || kind !== "all" ? "Try another search or type." : excluded.size ? "Every playable item is already included." : "Upload media in the library first."} />
        : <ul aria-label="Media results" aria-multiselectable={multiple} className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
          {results.map((media) => {
            const selected = selectedIds.has(media.id);
            const order = selection.findIndex((item) => item.id === media.id);
            return <li key={media.id}>
              <button
                type="button"
                aria-pressed={selected}
                aria-label={`${media.title || "Untitled media"}, ${mediaKindLabel(media)}`}
                onClick={() => toggle(media)}
                className={`group relative block w-full overflow-hidden rounded-lg border text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${selected ? "border-primary ring-2 ring-primary" : "border-border hover:border-foreground/30"}`}
              >
                <MediaThumbnail media={media} label={`${media.title || "Untitled media"} thumbnail`} className="w-full" />
                {selected && <span aria-hidden="true" className="absolute right-2 top-2 inline-flex h-6 min-w-6 items-center justify-center rounded-full bg-primary px-1.5 text-xs font-semibold text-primary-foreground">{multiple ? order + 1 : "✓"}</span>}
                <span className="block px-2.5 py-2"><span className="block truncate text-sm font-medium">{media.title || "Untitled media"}</span><span className="block truncate text-xs text-muted-foreground">{mediaKindLabel(media)}{media.artist ? ` · ${media.artist}` : ""}</span></span>
              </button>
            </li>;
          })}
        </ul>}
      {hasNextPage && <div ref={sentinel} className="flex justify-center"><Button type="button" variant="secondary" size="sm" disabled={isFetchingNextPage} onClick={() => void fetchNextPage()}>{isFetchingNextPage ? "Loading…" : "Load more"}</Button></div>}
    </div>
  </Modal>;
}

export interface MediaFieldProps {
  label: string;
  /** Selected asset id. */
  value?: string;
  onChange: (media: Media | undefined) => void;
  /** Label for the cleared state; when omitted the field cannot be cleared. */
  noneLabel?: string;
  disabled?: boolean;
  kinds?: MediaPickerProps["kinds"];
  className?: string;
}

/** Form field replacing a native media <select>: shows the current choice and opens a single-select MediaPicker. */
export function MediaField({ label, value, onChange, noneLabel, disabled = false, kinds, className }: MediaFieldProps) {
  const [open, setOpen] = React.useState(false);
  const [chosen, setChosen] = React.useState<Media | undefined>(undefined);
  const known = chosen?.assetId === value ? chosen : undefined;
  const lookup = useQuery({
    queryKey: ["controller", "media-picker", "asset", value],
    enabled: Boolean(value) && !known,
    queryFn: async () => readApiResult(await api.GET("/media", { params: { query: { assetId: [value!], limit: 1 } } })).items[0] ?? null,
  });
  const current = known ?? lookup.data ?? undefined;
  const labelId = React.useId();
  return <div className={className}>
    <span id={labelId} className="block text-sm font-medium">{label}</span>
    <div className="mt-1 flex items-center gap-2 rounded-md border border-border bg-background p-1.5">
      {value && <div className="w-16 shrink-0 overflow-hidden rounded">{current ? <MediaThumbnail media={current} label={`${current.title || "Media"} thumbnail`} className="w-full" /> : <div className="aspect-video w-full bg-muted" />}</div>}
      <span className="min-w-0 flex-1 truncate px-1 text-sm">{value ? current?.title || (lookup.isPending ? "Loading…" : "Untitled media") : <span className="text-muted-foreground">{noneLabel ?? "None selected"}</span>}</span>
      {value && noneLabel && <Button type="button" variant="ghost" size="sm" disabled={disabled} onClick={() => { setChosen(undefined); onChange(undefined); }}>Clear</Button>}
      <Button type="button" variant="secondary" size="sm" aria-haspopup="dialog" aria-describedby={labelId} disabled={disabled} onClick={() => setOpen(true)}>{value ? "Change" : "Choose"} {label.toLocaleLowerCase()}</Button>
    </div>
    <MediaPicker open={open} onOpenChange={setOpen} title={`Choose ${label.toLocaleLowerCase()}`} multiple={false} kinds={kinds} onConfirm={([media]) => { setChosen(media); onChange(media); }} />
  </div>;
}

export interface MediaTargetFieldProps {
  label: string;
  /** Selected media id (any kind, including web pages). */
  value?: string;
  onChange: (media: Media | undefined) => void;
  disabled?: boolean;
  className?: string;
}

/** Picks one media item of any kind by media id: the single "what to show" control for assignments and profiles. */
export function MediaTargetField({ label, value, onChange, disabled = false, className }: MediaTargetFieldProps) {
  const [open, setOpen] = React.useState(false);
  const [chosen, setChosen] = React.useState<Media | undefined>(undefined);
  const known = chosen?.id === value ? chosen : undefined;
  const lookup = useQuery({
    queryKey: ["controller", "media-detail-lookup", value],
    enabled: Boolean(value) && !known,
    queryFn: async () => readApiResult(await api.GET("/media/{mediaId}", { params: { path: { mediaId: value! } } })),
  });
  const current = known ?? lookup.data ?? undefined;
  const labelId = React.useId();
  return <div className={className}>
    <span id={labelId} className="block text-sm font-medium">{label}</span>
    <div className="mt-1 flex items-center gap-2 rounded-md border border-border bg-background p-1.5">
      {value && <div className="w-16 shrink-0 overflow-hidden rounded">{current ? <MediaThumbnail media={current} label={`${current.title || "Media"} thumbnail`} className="w-full" /> : <div className="aspect-video w-full bg-muted" />}</div>}
      <span className="min-w-0 flex-1 truncate px-1 text-sm">{value
        ? current ? <>{current.title || "Untitled media"} <span className="text-xs text-muted-foreground">· {mediaKindLabel(current)}</span></> : lookup.isError ? "Unavailable media" : "Loading…"
        : <span className="text-muted-foreground">Nothing selected</span>}</span>
      <Button type="button" variant="secondary" size="sm" aria-haspopup="dialog" aria-describedby={labelId} disabled={disabled} onClick={() => setOpen(true)}>{value ? "Change" : "Choose"} {label.toLocaleLowerCase()}</Button>
    </div>
    <MediaPicker open={open} onOpenChange={setOpen} title={`Choose ${label.toLocaleLowerCase()}`} description="Images, videos, and web pages." multiple={false} onConfirm={([media]) => { setChosen(media); onChange(media); }} />
  </div>;
}
