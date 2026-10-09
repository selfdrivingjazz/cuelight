import * as React from "react";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { type components } from "@cable/api-client";
import {
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  EmptyState,
  ErrorState,
  MediaCard,
  Pagination,
  PageFrame,
  PageHeader,
  PreviewMosaic,
  PreviewSurface,
  Skeleton,
  StatusBadge,
  TechnicalDetails,
  useToast,
  type NavigationLink,
  type PixelDimensions,
} from "@cable/controller-ui";
import { api, ApiRequestError, controllerBaseUrl, getApiToken, hasCapability, readApiResult } from "./api";
import { useAuth } from "./auth";
import { MediaDropzone } from "./components/MediaDropzone";
import { NodeAssignmentComposer } from "./assignment";
import { MediaThumbnail, previewKind, useMediaThumbnail } from "./media-thumbnail";
import { mediaKindLabel } from "./components/MediaPicker";

export type Media = components["schemas"]["Media"];
type MediaPage = components["schemas"]["MediaPage"];
type MediaPatch = components["schemas"]["MediaPatch"];
type MediaArtifact = NonNullable<Media["artifact"]>;
type MediaPlayback = NonNullable<Media["playback"]>;
type UsagePage = components["schemas"]["UsagePage"];
type MediaTypeFilter = "all" | "image" | "video" | "web";
export type MediaSortKey = "title" | "size" | "type";

/** URL state for the library. Defaults are omitted: no `sort` means date added, no `order` means newest first. */
export interface MediaLibrarySearch {
  cursor?: string;
  limit: number;
  search?: string;
  type: MediaTypeFilter;
  sort?: MediaSortKey;
  order?: "asc" | "desc";
  /** Opens the add form: `upload` for files, `web` for a page URL. */
  add?: "upload" | "web";
}

export function normalizeMediaSearch(search: Record<string, unknown>): MediaLibrarySearch {
  return {
    cursor: typeof search.cursor === "string" ? search.cursor : undefined,
    limit: typeof search.limit === "number" ? Math.min(Math.max(search.limit, 1), 48) : 24,
    search: typeof search.search === "string" && search.search.trim() ? search.search.trim() : undefined,
    type: search.type === "image" || search.type === "video" || search.type === "web" ? search.type : "all",
    sort: search.sort === "title" || search.sort === "size" || search.sort === "type" ? search.sort : undefined,
    order: search.order === "asc" ? "asc" : undefined,
    add: search.add === "upload" || search.add === "web" ? search.add : undefined,
  };
}

const mediaSortOptions: { value: string; label: string; sort?: MediaSortKey; order?: "asc" }[] = [
  { value: "createdAt:desc", label: "Newest first" },
  { value: "createdAt:asc", label: "Oldest first", order: "asc" },
  { value: "title:asc", label: "Name A–Z", sort: "title", order: "asc" },
  { value: "title:desc", label: "Name Z–A", sort: "title" },
  { value: "size:desc", label: "Largest first", sort: "size" },
  { value: "size:asc", label: "Smallest first", sort: "size", order: "asc" },
  { value: "type:asc", label: "Type A–Z", sort: "type", order: "asc" },
  { value: "type:desc", label: "Type Z–A", sort: "type" },
];

const RouterLink = Link as unknown as NavigationLink;
const fieldClass = "mt-1 h-9 w-full rounded-md border border-input bg-background px-3 text-sm focus-visible:ring-2 focus-visible:ring-ring";
const textareaClass = "mt-1 min-h-24 w-full rounded-md border border-input bg-background px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-ring";

function formatBytes(size: number): string {
  if (!Number.isFinite(size) || size < 0) return "Unknown";
  if (size < 1_000) return `${size.toLocaleString()} B`;
  if (size < 1_000_000) return `${(size / 1_000).toLocaleString(undefined, { maximumFractionDigits: 1 })} KB`;
  if (size < 1_000_000_000) return `${(size / 1_000_000).toLocaleString(undefined, { maximumFractionDigits: 1 })} MB`;
  return `${(size / 1_000_000_000).toLocaleString(undefined, { maximumFractionDigits: 1 })} GB`;
}

function QueryError({ error, retry }: { error: Error; retry: () => void }) {
  const status = error instanceof ApiRequestError ? error.status : undefined;
  return <ErrorState
    title={status === 403 ? "Access denied" : status === 404 ? "Media not found" : "Unable to load media"}
    description={error.message}
    onRetry={retry}
  />;
}

/** Fetches the original on demand. Only the detail page uses this: images to show full resolution, video once the operator presses play. */
function useOriginalMedia(media: Media, artifact: MediaArtifact, enabled: boolean) {
  return useQuery({
    queryKey: ["media-original", artifact.digest],
    enabled,
    staleTime: Infinity,
    gcTime: 60_000,
    retry: false,
    queryFn: async ({ signal }) => {
      const headers = new Headers({ Accept: artifact.mediaType });
      const token = getApiToken();
      if (token) headers.set("Authorization", `Bearer ${token}`);
      const response = await fetch(`${controllerBaseUrl.replace(/\/$/, "")}/media/${encodeURIComponent(media.id)}/content`, { headers, credentials: "omit", signal });
      if (!response.ok) throw new ApiRequestError(response.status, "The original could not be fetched.");
      const blob = await response.blob();
      return URL.createObjectURL(blob.type === artifact.mediaType ? blob : blob.slice(0, blob.size, artifact.mediaType));
    },
  });
}

/** Poster first, original second: the page is usable as soon as the thumbnail lands. */
function MediaHeroPreview({ media, artifact, playback, onDimensions }: { media: Media; artifact: MediaArtifact; playback: MediaPlayback; onDimensions: (dimensions: PixelDimensions) => void }) {
  const kind = previewKind(artifact.mediaType);
  const [playRequested, setPlayRequested] = React.useState(false);
  const thumbnail = useMediaThumbnail(media);
  const original = useOriginalMedia(media, artifact, kind === "image" || playRequested);
  const fit = playback.fit === "fill" ? "cover" : "contain";
  const frame = "bg-[repeating-conic-gradient(hsl(var(--muted))_0%_25%,transparent_0%_50%)] bg-[length:20px_20px]";

  if (!kind) return <PreviewSurface state="empty" emptyTitle="No visual preview" emptyDescription={artifact.mediaType} />;
  if (kind === "video" && playRequested && original.data) {
    return <PreviewSurface
      aria-label={`${media.title} player`}
      fit={fit}
      media={{ kind: "video", src: original.data, label: `${media.title} player`, poster: thumbnail.url, controls: true, autoPlay: true, muted: playback.muted, loop: playback.loop }}
      className="bg-black"
    />;
  }
  const still = kind === "image" ? original.data ?? thumbnail.url : thumbnail.url;
  return <div className="relative">
    <PreviewSurface
      aria-label={`${media.title} preview`}
      fit={fit}
      className={frame}
      media={still ? { kind: "image", src: still, alt: `${media.title} preview`, loading: "eager" } : undefined}
      state={still ? "ready" : original.isError ? "error" : thumbnail.status === "empty" && kind === "video" ? "empty" : "loading"}
      loadingLabel="Loading preview…"
      emptyTitle="No poster yet"
      emptyDescription="Play the video to preview it."
      errorDescription={original.error?.message}
    />
    {kind === "image" && original.data && <img className="hidden" src={original.data} alt="" onLoad={(event) => onDimensions({ width: event.currentTarget.naturalWidth, height: event.currentTarget.naturalHeight })} />}
    {kind === "video" && <div className="absolute inset-0 flex items-center justify-center">
      <Button type="button" size="lg" className="shadow-lg" disabled={playRequested && original.isFetching} onClick={() => setPlayRequested(true)}>
        {playRequested && original.isFetching ? "Loading video…" : original.isError ? "Retry video" : "▶ Play video"}
      </Button>
    </div>}
  </div>;
}

async function listMedia(search: MediaLibrarySearch): Promise<MediaPage> {
  return readApiResult(await api.GET("/media", {
    params: {
      query: {
        cursor: search.cursor,
        limit: search.limit,
        search: search.search,
        sort: search.sort ?? "createdAt",
        order: search.order ?? "desc",
        playable: true,
        kind: search.type === "web" ? "web" : undefined,
        mediaType: search.type === "image" || search.type === "video" ? search.type : undefined,
      },
    },
  }));
}

export async function getMedia(mediaId: string): Promise<{ media: Media; etag: string }> {
  const result = await api.GET("/media/{mediaId}", { params: { path: { mediaId } } });
  const media = readApiResult(result);
  return { media, etag: result.response.headers.get("ETag") ?? `"media:${media.id}:${media.version}"` };
}

async function getMediaUsage(mediaId: string): Promise<UsagePage> {
  return readApiResult(await api.GET("/usage/{kind}/{id}", { params: { path: { kind: "media", id: mediaId }, query: { limit: 100 } } }));
}

export function webHost(url: string | undefined): string {
  if (!url) return "";
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

function MediaLibraryCard({ media }: { media: Media }) {
  return <MediaCard
    title={media.title}
    artist={media.kind === "web" ? webHost(media.url) : media.artist}
    preview={<MediaThumbnail media={media} />}
    metadata={media.kind === "web"
      ? <span className="inline-flex items-center gap-1.5">{media.faviconUrl && <img src={media.faviconUrl} alt="" className="h-3.5 w-3.5 rounded-sm" loading="lazy" referrerPolicy="no-referrer" />}{mediaKindLabel(media)}</span>
      : <><span>{mediaKindLabel(media)}</span>{media.artifact && <span>{formatBytes(media.artifact.size)}</span>}</>}
    status={media.artifact?.state && media.artifact.state !== "ready" ? media.artifact.state : undefined}
    titleHref={`/library/media/${media.id}`}
    linkComponent={RouterLink}
  />;
}

function AppAccessToggle({ checked, disabled, onChange }: { checked: boolean; disabled?: boolean; onChange: (checked: boolean) => void }) {
  const helpId = React.useId();
  return <div className="flex items-start gap-2 text-sm">
    <input id={`${helpId}-input`} className="mt-0.5 h-4 w-4 accent-primary" type="checkbox" aria-describedby={helpId} checked={checked} disabled={disabled} onChange={(event) => onChange(event.currentTarget.checked)} />
    <span><label className="font-medium" htmlFor={`${helpId}-input`}>Cuelight app</label><span id={helpId} className="block text-xs text-muted-foreground">Give this page access to the screen's remote and the media library</span></span>
  </div>;
}

function AddWebPageForm({ onAdded }: { onAdded: (media: Media) => void }) {
  const [url, setUrl] = React.useState("");
  const [title, setTitle] = React.useState("");
  const [appAccess, setAppAccess] = React.useState(false);
  const mutation = useMutation({
    mutationFn: async () => {
      const value = url.trim();
      if (!/^https?:\/\//i.test(value)) throw new Error("Enter a web page URL starting with http:// or https://.");
      return readApiResult(await api.POST("/media", { params: { header: { "Idempotency-Key": crypto.randomUUID() } }, body: { kind: "web", url: value, title: title.trim() || undefined, appAccess } }));
    },
    onSuccess: onAdded,
  });
  return <form className="grid gap-3 sm:grid-cols-[2fr_1fr_auto] sm:items-end" onSubmit={(event) => { event.preventDefault(); mutation.mutate(); }}>
    <label className="block text-sm font-medium">Page URL<input className={fieldClass} type="url" inputMode="url" required placeholder="https://example.com" value={url} onChange={(event) => setUrl(event.currentTarget.value)} /></label>
    <label className="block text-sm font-medium">Title <span className="font-normal text-muted-foreground">(optional)</span><input className={fieldClass} value={title} maxLength={300} placeholder="Uses the page title" onChange={(event) => setTitle(event.currentTarget.value)} /></label>
    <Button type="submit" disabled={mutation.isPending || !url.trim()}>{mutation.isPending ? "Adding…" : "Add web page"}</Button>
    <div className="sm:col-span-3"><AppAccessToggle checked={appAccess} disabled={mutation.isPending} onChange={setAppAccess} /></div>
    {mutation.error instanceof Error && <p role="alert" className="text-sm text-destructive sm:col-span-3">{mutation.error.message}</p>}
  </form>;
}

function LibraryPending() {
  return <PreviewMosaic label="Loading media" columns={4}>{Array.from({ length: 8 }, (_, index) => <Skeleton className="aspect-video w-full" key={index} />)}</PreviewMosaic>;
}

export function MediaLibraryPage() {
  const search = useSearch({ strict: false }) as MediaLibrarySearch;
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const auth = useAuth();
  const [searchDraft, setSearchDraft] = React.useState(search.search ?? "");
  const query = useQuery({
    queryKey: ["controller", "media", search],
    queryFn: () => listMedia(search),
    placeholderData: keepPreviousData,
    // Web pages still being captured poll until their thumbnail lands.
    refetchInterval: (current) => current.state.data?.items.some((item) => item.kind === "web" && !item.thumbnail && !item.thumbnailError) ? 3000 : false,
  });
  const canAdd = Boolean(auth.operator && hasCapability(auth.operator, "media_write", "media_write"));
  const adding = search.add;
  const setAdding = (add: MediaLibrarySearch["add"]) => void navigate({ to: "/library/media", search: { ...search, add } });

  React.useEffect(() => setSearchDraft(search.search ?? ""), [search.search]);

  const updateSearch = (patch: Partial<MediaLibrarySearch>) => void navigate({
    to: "/library/media",
    search: { ...search, ...patch, cursor: undefined, add: undefined },
  });

  const items = query.data?.items ?? [];
  const hasFilters = Boolean(search.search || search.type !== "all");
  const clearSearch = () => {
    setSearchDraft("");
    updateSearch({ search: undefined, type: "all" });
  };

  return <PageFrame>
    <PageHeader title="Media">
      {canAdd && <Button type="button" variant={adding === "web" ? "secondary" : "ghost"} aria-expanded={adding === "web"} aria-controls="media-add-web" onClick={() => setAdding(adding === "web" ? undefined : "web")}>{adding === "web" ? "Close" : "Add web page"}</Button>}
      {canAdd && <Button type="button" variant={adding === "upload" ? "secondary" : "primary"} aria-expanded={adding === "upload"} aria-controls="media-upload" onClick={() => setAdding(adding === "upload" ? undefined : "upload")}>{adding === "upload" ? "Close upload" : "Add media"}</Button>}
    </PageHeader>

    {adding === "upload" && canAdd && <Card id="media-upload">
      <CardHeader><CardTitle>Add an image or video</CardTitle></CardHeader>
      <CardContent><MediaDropzone onUploaded={(media) => {
        void queryClient.invalidateQueries({ queryKey: ["controller", "media"] });
        void navigate({ to: "/library/media/$mediaId", params: { mediaId: media.id } });
      }} /></CardContent>
    </Card>}
    {adding === "web" && canAdd && <Card id="media-add-web">
      <CardHeader><CardTitle>Add a web page</CardTitle></CardHeader>
      <CardContent><AddWebPageForm onAdded={(media) => {
        void queryClient.invalidateQueries({ queryKey: ["controller", "media"] });
        void navigate({ to: "/library/media/$mediaId", params: { mediaId: media.id } });
      }} /></CardContent>
    </Card>}

    <div className="space-y-2">
      <form className="flex flex-wrap items-center gap-2" role="search" aria-label="Filter media" onSubmit={(event) => {
        event.preventDefault();
        updateSearch({ search: searchDraft.trim() || undefined });
      }}>
        <label className="min-w-0 basis-full sm:basis-64 sm:flex-1">
          <span className="sr-only">Search media</span>
          <input className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm focus-visible:ring-2 focus-visible:ring-ring" type="search" value={searchDraft} onChange={(event) => {
            const value = event.currentTarget.value;
            setSearchDraft(value);
            if (!value && search.search) updateSearch({ search: undefined });
          }} placeholder="Search title, artist, or URL" />
        </label>
        <label className="min-w-0 flex-1 sm:flex-none">
          <span className="sr-only">Media type</span>
          <select className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm focus-visible:ring-2 focus-visible:ring-ring sm:w-44" value={search.type} onChange={(event) => updateSearch({ type: event.currentTarget.value as MediaTypeFilter })}>
            <option value="all">All media</option>
            <option value="image">Images</option>
            <option value="video">Videos</option>
            <option value="web">Web pages</option>
          </select>
        </label>
        <label className="min-w-0 flex-1 sm:flex-none">
          <span className="sr-only">Sort media</span>
          <select className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm focus-visible:ring-2 focus-visible:ring-ring sm:w-44" value={`${search.sort ?? "createdAt"}:${search.order ?? "desc"}`} onChange={(event) => {
            const option = mediaSortOptions.find((candidate) => candidate.value === event.currentTarget.value);
            updateSearch({ sort: option?.sort, order: option?.order });
          }}>
            {mediaSortOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
          </select>
        </label>
        <Button type="submit" variant="secondary">Search</Button>
        {(hasFilters || searchDraft || search.cursor) && <Button type="button" variant="ghost" onClick={clearSearch}>Reset</Button>}
      </form>
      <p role="status" className="text-xs text-muted-foreground">
        {query.isFetching ? "Updating media…" : query.isError ? "Media could not be loaded" : `${items.length.toLocaleString()} ${items.length === 1 ? "item" : "items"} on this page`}
      </p>
    </div>

    {query.isError ? <QueryError error={query.error} retry={() => void query.refetch()} /> : query.isPending || (query.isPlaceholderData && items.length === 0) ? <LibraryPending /> : items.length > 0 ? <>
      <div aria-busy={query.isFetching}><PreviewMosaic label="Media" columns={4}>{items.map((media) => <MediaLibraryCard media={media} key={media.id} />)}</PreviewMosaic></div>
      {!query.isPlaceholderData && <Pagination
        page={query.data.page}
        onPrevious={() => void navigate({ to: "/library/media", search: { ...search, cursor: query.data.page.previousCursor ?? undefined } })}
        onNext={() => void navigate({ to: "/library/media", search: { ...search, cursor: query.data.page.nextCursor ?? undefined } })}
      />}
    </> : <EmptyState
      title={hasFilters ? "No matching media" : "No media yet"}
      description={hasFilters ? "Try another search or reset your filters." : canAdd ? "Use Add media to upload an image or video, or Add web page to put a site on a screen." : "Images, videos, and web pages will appear here when they are added."}
      action={hasFilters ? <Button type="button" variant="secondary" onClick={clearSearch}>Reset filters</Button> : undefined}
    />}
  </PageFrame>;
}

interface MediaFormState {
  title: string;
  artist: string;
  description: string;
  tags: string;
  fit: "fit" | "fill";
  loop: boolean;
  muted: boolean;
  volume: number;
  orientation: 0 | 90 | 180 | 270;
}

function formFromMedia(media: Media): MediaFormState {
  return {
    title: media.title,
    artist: media.artist ?? "",
    description: media.description ?? "",
    tags: media.tags.join(", "),
    ...media.playback!,
  };
}

function MediaEditor({ media, etag }: { media: Media; etag: string }) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const [form, setForm] = React.useState<MediaFormState>(() => formFromMedia(media));
  React.useEffect(() => setForm(formFromMedia(media)), [media]);
  const mutation = useMutation({
    mutationFn: async (body: MediaPatch) => {
      const result = await api.PATCH("/media/{mediaId}", {
        params: { path: { mediaId: media.id }, header: { "If-Match": etag, "Idempotency-Key": crypto.randomUUID() } },
        body,
      });
      const updated = readApiResult(result);
      return { media: updated, etag: result.response.headers.get("ETag") ?? `"media:${updated.id}:${updated.version}"` };
    },
    onSuccess: (updated) => {
      queryClient.setQueryData(["controller", "media-detail", media.id], updated);
      void queryClient.invalidateQueries({ queryKey: ["controller", "media"] });
      toast.success("Changes saved.");
    },
  });
  const set = <K extends keyof MediaFormState>(key: K, value: MediaFormState[K]) => setForm((current) => ({ ...current, [key]: value }));
  const isVideo = previewKind(media.artifact!.mediaType) === "video";

  return <Card>
    <CardHeader><CardTitle>Details & playback</CardTitle></CardHeader>
    <CardContent>
      <form className="space-y-5" onSubmit={(event) => {
        event.preventDefault();
        mutation.mutate({
          title: form.title.trim(),
          artist: form.artist.trim(),
          description: form.description.trim(),
          tags: form.tags.split(",").map((tag) => tag.trim()).filter(Boolean),
          playback: { fit: form.fit, loop: form.loop, muted: form.muted, volume: form.muted ? 0 : form.volume, orientation: form.orientation },
        });
      }}>
        <div className="grid gap-4 sm:grid-cols-2">
          <label className="text-sm font-medium">Title<input className={fieldClass} required value={form.title} onChange={(event) => set("title", event.currentTarget.value)} /></label>
          <label className="text-sm font-medium">Artist<input className={fieldClass} value={form.artist} onChange={(event) => set("artist", event.currentTarget.value)} /></label>
        </div>
        <label className="block text-sm font-medium">Description<textarea className={textareaClass} value={form.description} onChange={(event) => set("description", event.currentTarget.value)} /></label>
        <label className="block text-sm font-medium">Tags<input className={fieldClass} value={form.tags} onChange={(event) => set("tags", event.currentTarget.value)} placeholder="lobby, seasonal, portrait" /><span className="mt-1 block text-xs font-normal text-muted-foreground">Separate tags with commas.</span></label>
        <fieldset className="space-y-4 border-t border-border pt-4">
          <legend className="pr-3 text-sm font-semibold">Playback defaults</legend>
          <div className={`grid gap-4 ${isVideo ? "sm:grid-cols-3" : "sm:grid-cols-2"}`}>
            <label className="text-sm font-medium">Fit<select className={fieldClass} value={form.fit} onChange={(event) => set("fit", event.currentTarget.value as "fit" | "fill")}><option value="fit">Fit entire frame</option><option value="fill">Fill frame</option></select></label>
            <label className="text-sm font-medium">Orientation<select className={fieldClass} value={form.orientation} onChange={(event) => set("orientation", Number(event.currentTarget.value) as MediaFormState["orientation"])}><option value={0}>0°</option><option value={90}>90°</option><option value={180}>180°</option><option value={270}>270°</option></select></label>
            {isVideo && <label className="text-sm font-medium">Volume<input className={fieldClass} type="number" min={0} max={100} step={1} disabled={form.muted} value={form.volume} onChange={(event) => set("volume", Number(event.currentTarget.value))} /></label>}
          </div>
          {isVideo && <div className="flex flex-wrap gap-5">
            <label className="inline-flex items-center gap-2 text-sm font-medium"><input className="h-4 w-4 accent-primary" type="checkbox" checked={form.loop} onChange={(event) => set("loop", event.currentTarget.checked)} />Loop playback</label>
            <label className="inline-flex items-center gap-2 text-sm font-medium"><input className="h-4 w-4 accent-primary" type="checkbox" checked={form.muted} onChange={(event) => set("muted", event.currentTarget.checked)} />Muted</label>
          </div>}
        </fieldset>
        {mutation.isError && <ErrorState title={mutation.error instanceof ApiRequestError && mutation.error.status === 412 ? "Media changed elsewhere" : "Could not save changes"} description={mutation.error.message} />}
        <div className="flex flex-wrap gap-2"><Button type="submit" disabled={mutation.isPending || !form.title.trim()}>{mutation.isPending ? "Saving…" : "Save changes"}</Button><Button type="button" variant="ghost" disabled={mutation.isPending} onClick={() => { setForm(formFromMedia(media)); mutation.reset(); }}>Discard changes</Button></div>
      </form>
    </CardContent>
  </Card>;
}

function UsageSummary({ usage, pending, error, retry }: { usage?: UsagePage; pending: boolean; error?: Error; retry: () => void }) {
  if (pending) return <Card><CardContent className="space-y-3 pt-5"><Skeleton className="h-5 w-36" /><Skeleton className="h-12 w-full" /></CardContent></Card>;
  if (error) return <QueryError error={error} retry={retry} />;
  const references = usage?.references ?? [];
  const playlists = references.filter((reference) => reference.kind === "playlist").length;
  const releases = references.filter((reference) => reference.kind === "release").length;
  return <Card>
    <CardHeader><CardTitle>Used in</CardTitle></CardHeader>
    <CardContent className="space-y-4">
      {references.length > 0 && <p className="text-sm text-muted-foreground">{playlists} {playlists === 1 ? "playlist" : "playlists"} · {releases} {releases === 1 ? "release" : "releases"}</p>}
      {references.length > 0
        ? <ul className="space-y-2 border-t border-border pt-4">{references.map((reference) => <li key={`${reference.kind}:${reference.id}`}><a className="text-sm font-medium text-primary hover:underline" href={reference.href}>{reference.name}</a><span className="ml-2 text-xs capitalize text-muted-foreground">{reference.kind}</span></li>)}</ul>
        : <p className="text-sm text-muted-foreground">Not used by any playlist or release.</p>}
    </CardContent>
  </Card>;
}
type Playlist = components["schemas"]["Playlist"];

function MediaPlaylistAdder({ media }: { media: Media }) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const playlists = useQuery({
    queryKey: ["controller", "media-playlist-options"],
    queryFn: async () => readApiResult(await api.GET("/playlists", { params: { query: { limit: 100 } } })),
  });
  const [playlistId, setPlaylistId] = React.useState("");
  const [durationSeconds, setDurationSeconds] = React.useState(60);
  const mutation = useMutation({
    mutationFn: async () => {
      if (!playlistId) throw new Error("Choose a playlist.");
      const playlist = readApiResult(await api.GET("/playlists/{playlistId}", { params: { path: { playlistId } } })) as Playlist;
      const position = playlist.items.reduce((highest, item) => Math.max(highest, item.position), 0) + 1;
      return readApiResult(await api.PATCH("/playlists/{playlistId}", {
        params: { path: { playlistId }, header: { "If-Match": `"v${playlist.version}"` } },
        body: {
          name: playlist.name,
          description: playlist.description,
          items: [
            ...playlist.items.map((item) => ({
              ...(item.mediaId ? { mediaId: item.mediaId } : { assetId: item.assetId }),
              position: item.position,
              durationSeconds: item.durationSeconds,
              title: item.title,
              subtitle: item.subtitle,
            })),
            { mediaId: media.id, position, durationSeconds, title: media.title },
          ],
        },
      }));
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["authoring", "playlists"] });
      void queryClient.invalidateQueries({ queryKey: ["authoring", "playlist", playlistId] });
      toast.success("Added to playlist.");
    },
  });

  return <Card>
    <CardHeader><CardTitle>Add to playlist</CardTitle></CardHeader>
    <CardContent className="space-y-4">
      <label className="block text-sm font-medium">Playlist
        <select className={fieldClass} value={playlistId} onChange={(event) => setPlaylistId(event.currentTarget.value)} disabled={playlists.isPending || mutation.isPending}>
          <option value="">Choose a playlist</option>
          {playlists.data?.items.map((playlist) => <option key={playlist.id} value={playlist.id}>{playlist.name}</option>)}
        </select>
      </label>
      <label className="block text-sm font-medium">Play duration
        <span className="flex items-center gap-2"><input className={fieldClass} type="number" min={1} max={86400} value={durationSeconds} onChange={(event) => setDurationSeconds(Math.max(1, Number(event.currentTarget.value)))} disabled={mutation.isPending} /><span className="text-sm text-muted-foreground">seconds</span></span>
      </label>
      {playlists.isError && <ErrorState title="Playlists unavailable" description={playlists.error.message} onRetry={() => void playlists.refetch()} />}
      {mutation.isError && <ErrorState title="Could not add media" description={mutation.error.message} />}
      <Button type="button" variant="secondary" onClick={() => mutation.mutate()} disabled={!playlistId || mutation.isPending}>{mutation.isPending ? "Adding…" : "Add to playlist"}</Button>
    </CardContent>
  </Card>;
}


export function MediaDetailPage() {
  const { mediaId = "" } = useParams({ strict: false });
  const mediaQuery = useQuery({
    queryKey: ["controller", "media-detail", mediaId],
    queryFn: () => getMedia(mediaId),
    refetchInterval: (current) => { const media = current.state.data?.media; return media?.kind === "web" && !media.thumbnail && !media.thumbnailError ? 3000 : false; },
  });
  if (mediaQuery.isPending) return <PageFrame><Skeleton className="h-8 w-48" /><Skeleton className="aspect-video w-full" /></PageFrame>;
  if (mediaQuery.isError) return <PageFrame><QueryError error={mediaQuery.error} retry={() => void mediaQuery.refetch()} /></PageFrame>;
  const { media, etag } = mediaQuery.data;
  if (media.kind === "web") return <WebMediaDetail media={media} etag={etag} />;
  if (!media.artifact || !media.playback) return <PageFrame><ErrorState title="Media file unavailable" description="This media has no file attached." /></PageFrame>;
  return <FileMediaDetail media={media} artifact={media.artifact} playback={media.playback} etag={etag} />;
}

function relativeTime(value: string): string {
  const seconds = Math.round((Date.now() - new Date(value).getTime()) / 1000);
  if (!Number.isFinite(seconds)) return "";
  if (seconds < 60) return "just now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86_400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86_400)}d ago`;
}

/** A web page: capture preview, assignment, title/app access editing, recapture, and removal. */
function WebMediaDetail({ media, etag }: { media: Media; etag: string }) {
  const auth = useAuth();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toast = useToast();
  const canWrite = Boolean(auth.operator && hasCapability(auth.operator, "media_write", "media_write"));
  const canAssign = Boolean(auth.operator && hasCapability(auth.operator, "assignment_write", "assignment_write"));
  const [title, setTitle] = React.useState(media.title);
  const [appAccess, setAppAccess] = React.useState(media.appAccess);
  React.useEffect(() => setTitle(media.title), [media.title]);
  React.useEffect(() => setAppAccess(media.appAccess), [media.appAccess]);
  const store = (updated: Media, nextEtag?: string) => {
    queryClient.setQueryData(["controller", "media-detail", media.id], { media: updated, etag: nextEtag ?? `"media:${updated.id}:${updated.version}"` });
    void queryClient.invalidateQueries({ queryKey: ["controller", "media"] });
  };
  const refresh = useMutation({
    mutationFn: async () => readApiResult(await api.POST("/media/{mediaId}/refresh", { params: { path: { mediaId: media.id } } })),
    onSuccess: (updated) => store(updated),
  });
  const rename = useMutation({
    mutationFn: async () => {
      const result = await api.PATCH("/media/{mediaId}", { params: { path: { mediaId: media.id }, header: { "If-Match": etag, "Idempotency-Key": crypto.randomUUID() } }, body: { title: title.trim(), appAccess } });
      return { media: readApiResult(result), etag: result.response.headers.get("ETag") ?? undefined };
    },
    onSuccess: (updated) => { store(updated.media, updated.etag); toast.success("Changes saved."); },
  });
  const remove = useMutation({
    mutationFn: async () => {
      const result = await api.DELETE("/media/{mediaId}", { params: { path: { mediaId: media.id } } });
      if (!result.response.ok) throw new ApiRequestError(result.response.status, "The web page could not be removed.");
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["controller", "media"] });
      toast.success("Web page removed.");
      void navigate({ to: "/library/media", search: { limit: 24, type: "all" } });
    },
  });
  const error = refresh.error ?? rename.error ?? remove.error;

  return <PageFrame>
    <PageHeader title={media.title} description={media.url} meta={<p className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">{media.appAccess && <StatusBadge tone="info">Cuelight app</StatusBadge>}<span>{mediaKindLabel(media)} · Added {new Date(media.createdAt).toLocaleDateString(undefined, { dateStyle: "medium" })}</span></p>}>
      {media.url && <Button type="button" variant="secondary" onClick={() => window.open(media.url, "_blank", "noopener,noreferrer")}>Open page</Button>}
      {canWrite && <Button type="button" variant="secondary" disabled={refresh.isPending} onClick={() => refresh.mutate()}>{refresh.isPending ? "Capturing…" : "Recapture"}</Button>}
    </PageHeader>
    <MediaThumbnail media={media} label={`${media.title} page capture`} className="max-h-[70vh]" />
    <p className="text-xs text-muted-foreground">
      {media.thumbnail
        ? <>Captured {media.thumbnail.capturedAt ? relativeTime(media.thumbnail.capturedAt) : ""} {media.thumbnail.source === "node" ? "from a screen playing this page" : "by the controller at 1920×1080"}.</>
        : media.thumbnailError ? <>Last capture failed: {media.thumbnailError}</> : "Capturing the page…"}
    </p>
    <div className="grid items-start gap-4 lg:grid-cols-2">
      {canAssign && <NodeAssignmentComposer title="Show on a screen" fixedMedia={media} />}
      <Card>
        <CardHeader><CardTitle>Details</CardTitle></CardHeader>
        <CardContent className="space-y-4">
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
            {media.url && <><dt className="text-muted-foreground">URL</dt><dd className="break-all"><a className="hover:text-primary" href={media.url} target="_blank" rel="noreferrer noopener">{media.url}</a></dd></>}
            <dt className="text-muted-foreground">Added</dt><dd>{new Date(media.createdAt).toLocaleString()}</dd>
          </dl>
          {canWrite && <form className="space-y-2" onSubmit={(event) => { event.preventDefault(); rename.mutate(); }}>
            <label className="block text-sm font-medium">Title<input className={fieldClass} value={title} maxLength={300} required onChange={(event) => setTitle(event.currentTarget.value)} /></label>
            <AppAccessToggle checked={appAccess} disabled={rename.isPending} onChange={setAppAccess} />
            <div className="flex flex-wrap gap-2">
              <Button type="submit" variant="secondary" disabled={rename.isPending || !title.trim() || (title.trim() === media.title && appAccess === media.appAccess)}>{rename.isPending ? "Saving…" : "Save changes"}</Button>
              <Button type="button" variant="ghost" disabled={remove.isPending} onClick={() => { if (window.confirm("Remove this web page from the library? Screens already showing it keep playing.")) remove.mutate(); }}>Remove from library</Button>
            </div>
          </form>}
          {error instanceof Error && <p role="alert" className="text-sm text-destructive">{error.message}</p>}
        </CardContent>
      </Card>
    </div>
  </PageFrame>;
}

function FileMediaDetail({ media, artifact, playback, etag }: { media: Media; artifact: MediaArtifact; playback: MediaPlayback; etag: string }) {
  const auth = useAuth();
  const usageQuery = useQuery({ queryKey: ["controller", "media-usage", media.id], queryFn: () => getMediaUsage(media.id) });
  const [measured, setMeasured] = React.useState<PixelDimensions>();
  const [assigning, setAssigning] = React.useState(false);
  const [addingToPlaylist, setAddingToPlaylist] = React.useState(false);
  const canEdit = Boolean(auth.operator && hasCapability(auth.operator, "media_write", "media_write"));
  const kind = previewKind(artifact.mediaType);
  const dimensions = measured;
  const format = artifact.mediaType.split("/")[1]?.toUpperCase() ?? artifact.mediaType;
  const summary = [kind === "image" ? "Image" : kind === "video" ? "Video" : "File", format, formatBytes(artifact.size), dimensions && `${dimensions.width.toLocaleString()} × ${dimensions.height.toLocaleString()} px`, `Added ${new Date(media.createdAt).toLocaleDateString(undefined, { dateStyle: "medium" })}`].filter(Boolean);
  return <PageFrame>
    <PageHeader
      title={media.title}
      description={media.artist}
      meta={<p className="text-sm text-muted-foreground">{summary.join(" · ")}</p>}
    >
      {canEdit && <Button type="button" variant="secondary" aria-expanded={addingToPlaylist} aria-controls="media-playlist-picker" onClick={() => { setAddingToPlaylist((value) => !value); setAssigning(false); }}>{addingToPlaylist ? "Close playlist picker" : "Add to playlist"}</Button>}
      <Button type="button" variant={assigning ? "secondary" : "primary"} aria-expanded={assigning} aria-controls="media-assignment" onClick={() => { setAssigning((value) => !value); setAddingToPlaylist(false); }}>{assigning ? "Close assignment" : "Assign to display"}</Button>
    </PageHeader>
    {addingToPlaylist && <div id="media-playlist-picker"><MediaPlaylistAdder media={media} /></div>}
    {assigning && <div id="media-assignment"><NodeAssignmentComposer fixedMedia={media} title="Assign media" /></div>}

    <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,2fr)_minmax(17rem,1fr)]">
      <div className="min-w-0 space-y-6">
        <MediaHeroPreview media={media} artifact={artifact} playback={playback} onDimensions={setMeasured} />
        {artifact.state && artifact.state !== "ready" && <ErrorState title={artifact.state === "missing" ? "Media file missing" : "Media file quarantined"} description="This media file is not available for playback." />}
        {canEdit ? <MediaEditor media={media} etag={etag} /> : <Card>
          <CardHeader><CardTitle>Media details</CardTitle></CardHeader>
          <CardContent className="space-y-3">
            {media.description && <p className="text-sm">{media.description}</p>}
            {media.tags.length > 0 && <div className="flex flex-wrap gap-2">{media.tags.map((tag) => <span className="rounded-full bg-muted px-2 py-1 text-xs" key={tag}>{tag}</span>)}</div>}
            <p className="text-sm text-muted-foreground">Your operator permissions allow viewing, but not editing this media.</p>
          </CardContent>
        </Card>}
      </div>
      <aside className="min-w-0 space-y-4 lg:sticky lg:top-6" aria-label="Media context">
        <UsageSummary usage={usageQuery.data} pending={usageQuery.isPending} error={usageQuery.error ?? undefined} retry={() => void usageQuery.refetch()} />
        <TechnicalDetails items={[
          { label: "MIME type", value: artifact.mediaType },
          { label: "Size", value: `${formatBytes(artifact.size)} (${artifact.size.toLocaleString()} bytes)` },
          ...(dimensions ? [{ label: "Pixels", value: `${dimensions.width.toLocaleString()} × ${dimensions.height.toLocaleString()} px` }] : []),
          { label: "Version", value: media.version.toLocaleString() },
          { label: "Asset", value: <span className="font-mono text-xs break-all">{media.assetId}</span> },
          { label: "Digest", value: <span className="font-mono text-xs break-all">{artifact.digest}</span> },
        ]} />
      </aside>
    </div>
  </PageFrame>;
}
