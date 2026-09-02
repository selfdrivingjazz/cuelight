import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { type components } from "@cable/api-client";
import {
  Breadcrumbs,
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  EmptyState,
  ErrorState,
  MediaCard,
  Pagination,
  PreviewMosaic,
  PreviewSurface,
  Skeleton,
  TechnicalDetails,
  type NavigationLink,
  type PixelDimensions,
} from "@cable/controller-ui";
import { api, ApiRequestError, controllerBaseUrl, hasCapability, readApiResult } from "./api";
import { useAuth } from "./auth";
import { MediaDropzone } from "./components/MediaDropzone";
import { NodeAssignmentComposer } from "./assignment";

export type Media = components["schemas"]["Media"];
type MediaPage = components["schemas"]["MediaPage"];
type MediaPatch = components["schemas"]["MediaPatch"];
type MediaUsage = components["schemas"]["MediaUsage"];
type MediaTypeFilter = "all" | "image" | "video";

export interface MediaLibrarySearch {
  cursor?: string;
  limit: number;
  search?: string;
  type: MediaTypeFilter;
}

export function normalizeMediaSearch(search: Record<string, unknown>): MediaLibrarySearch {
  return {
    cursor: typeof search.cursor === "string" ? search.cursor : undefined,
    limit: typeof search.limit === "number" ? Math.min(Math.max(search.limit, 1), 48) : 24,
    search: typeof search.search === "string" && search.search.trim() ? search.search.trim() : undefined,
    type: search.type === "image" || search.type === "video" ? search.type : "all",
  };
}

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

function mediaKind(mediaType: string): "image" | "video" | null {
  if (mediaType.startsWith("image/")) return "image";
  if (mediaType.startsWith("video/")) return "video";
  return null;
}

function QueryError({ error, retry }: { error: Error; retry: () => void }) {
  const status = error instanceof ApiRequestError ? error.status : undefined;
  return <ErrorState
    title={status === 403 ? "Access denied" : status === 404 ? "Media not found" : "Unable to load media"}
    description={error.message}
    onRetry={retry}
  />;
}

function PageFrame({ children, breadcrumbs }: { children: React.ReactNode; breadcrumbs: Array<{ label: string; href?: string }> }) {
  return <main className="mx-auto max-w-[var(--content-max-width)] space-y-6 p-4 md:p-8">
    <Breadcrumbs items={breadcrumbs} linkComponent={RouterLink} />
    {children}
  </main>;
}

function PageHeader({ title, description, children }: { title: string; description?: string; children?: React.ReactNode }) {
  return <div className="flex flex-col justify-between gap-4 border-b border-border pb-5 md:flex-row md:items-end">
    <div>
      <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
      {description && <p className="mt-1 text-sm text-muted-foreground">{description}</p>}
    </div>
    {children && <div className="flex flex-wrap gap-2">{children}</div>}
  </div>;
}

interface PreviewState {
  status: "loading" | "ready" | "error";
  url?: string;
  message?: string;
}

export function useAuthenticatedPreview(media: Media) {
  const { token } = useAuth();
  const [attempt, setAttempt] = React.useState(0);
  const [preview, setPreview] = React.useState<PreviewState>({ status: "loading" });

  React.useEffect(() => {
    const controller = new AbortController();
    let objectUrl: string | undefined;
    setPreview({ status: "loading" });

    const headers = new Headers({ Accept: media.artifact.mediaType });
    if (token) headers.set("Authorization", `Bearer ${token}`);
    const base = controllerBaseUrl.replace(/\/$/, "");

    void fetch(`${base}/media/${encodeURIComponent(media.id)}/content`, {
      method: "GET",
      headers,
      credentials: "omit",
      cache: "no-store",
      signal: controller.signal,
    }).then(async (response) => {
      if (!response.ok) throw new ApiRequestError(response.status, "The preview could not be fetched.");
      const blob = await response.blob();
      if (controller.signal.aborted) return;
      const typedBlob = blob.type === media.artifact.mediaType ? blob : blob.slice(0, blob.size, media.artifact.mediaType);
      objectUrl = URL.createObjectURL(typedBlob);
      setPreview({ status: "ready", url: objectUrl });
    }).catch((error: unknown) => {
      if (controller.signal.aborted) return;
      setPreview({ status: "error", message: error instanceof Error ? error.message : "The preview could not be fetched." });
    });

    return () => {
      controller.abort();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [attempt, media.artifact.mediaType, media.id, media.version, token]);

  return { ...preview, retry: () => setAttempt((value) => value + 1) };
}

function AuthenticatedMediaPreview({ media, hero = false, onDimensions }: { media: Media; hero?: boolean; onDimensions?: (dimensions?: PixelDimensions) => void }) {
  const preview = useAuthenticatedPreview(media);
  const container = React.useRef<HTMLDivElement>(null);
  const kind = mediaKind(media.artifact.mediaType);
  const reportDimensions = React.useCallback(() => {
    const element = container.current?.querySelector("img, video");
    if (element instanceof HTMLImageElement && element.naturalWidth > 0 && element.naturalHeight > 0) {
      onDimensions?.({ width: element.naturalWidth, height: element.naturalHeight });
    } else if (element instanceof HTMLVideoElement && element.videoWidth > 0 && element.videoHeight > 0) {
      onDimensions?.({ width: element.videoWidth, height: element.videoHeight });
    }
  }, [onDimensions]);

  if (!kind) {
    return <PreviewSurface
      state="empty"
      aspectRatio="16 / 9"
      emptyTitle="Preview not supported"
      emptyDescription={media.artifact.mediaType}
    />;
  }

  const mediaSource = preview.url
    ? kind === "image"
      ? { kind: "image" as const, src: preview.url, alt: `${media.title} preview`, loading: hero ? "eager" as const : "lazy" as const, onLoad: reportDimensions }
      : { kind: "video" as const, src: preview.url, label: `${media.title} preview`, controls: hero, muted: media.playback.muted, loop: media.playback.loop, autoPlay: false, onLoad: reportDimensions }
    : undefined;

  return <div ref={container}>
    <PreviewSurface
      media={mediaSource}
      state={preview.status}
      aspectRatio="16 / 9"
      fit={media.playback.fit === "fill" ? "cover" : "contain"}
      className={hero ? "max-h-[70vh]" : undefined}
      loadingLabel={`Loading ${media.title} preview…`}
      errorDescription={preview.message}
      onRetry={preview.retry}
      aria-label={`${media.title} visual preview`}
    />
  </div>;
}

async function listMedia(search: MediaLibrarySearch): Promise<MediaPage> {
  return readApiResult(await api.GET("/media", {
    params: {
      query: {
        cursor: search.cursor,
        limit: search.limit,
        search: search.search,
        playable: true,
        kind: search.type === "all" ? undefined : search.type,
      },
    },
  }));
}

export async function getMedia(mediaId: string): Promise<{ media: Media; etag: string }> {
  const result = await api.GET("/media/{mediaId}", { params: { path: { mediaId } } });
  const media = readApiResult(result);
  return { media, etag: result.response.headers.get("ETag") ?? `"media:${media.id}:${media.version}"` };
}

async function getMediaUsage(mediaId: string): Promise<MediaUsage> {
  return readApiResult(await api.GET("/media/{mediaId}/usage", { params: { path: { mediaId } } }));
}

function MediaLibraryCard({ media }: { media: Media }) {
  const [dimensions, setDimensions] = React.useState<PixelDimensions>();
  const kind = mediaKind(media.artifact.mediaType);
  return <MediaCard
    title={media.title}
    artist={media.artist}
    description={media.description}
    preview={<AuthenticatedMediaPreview media={media} onDimensions={setDimensions} />}
    dimensions={dimensions}
    metadata={<><span>{kind === "image" ? "Image" : kind === "video" ? "Video" : media.artifact.mediaType}</span><span>{formatBytes(media.artifact.size)}</span></>}
    status={media.artifact.state ?? "ready"}
    titleHref={`/media/${media.id}`}
    linkComponent={RouterLink}
    actions={<Button asChild size="sm"><Link to="/media/$mediaId" params={{ mediaId: media.id }}>Open</Link></Button>}
  />;
}

function LibraryPending() {
  return <PageFrame breadcrumbs={[{ label: "Library" }, { label: "Media" }]}>
    <div className="space-y-3" aria-busy="true"><Skeleton className="h-8 w-48" /><Skeleton className="h-10 w-full max-w-md" /></div>
    <PreviewMosaic label="Loading media" columns={4}>{Array.from({ length: 8 }, (_, index) => <Skeleton className="aspect-video w-full" key={index} />)}</PreviewMosaic>
  </PageFrame>;
}

export function MediaLibraryPage() {
  const search = useSearch({ strict: false }) as MediaLibrarySearch;
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const auth = useAuth();
  const [searchDraft, setSearchDraft] = React.useState(search.search ?? "");
  const [adding, setAdding] = React.useState(false);
  const query = useQuery({ queryKey: ["controller", "media", search], queryFn: () => listMedia(search) });
  const canAdd = Boolean(auth.operator && hasCapability(auth.operator, "media_write", "media_write"));

  React.useEffect(() => setSearchDraft(search.search ?? ""), [search.search]);
  if (query.isPending) return <LibraryPending />;

  const updateSearch = (patch: Partial<MediaLibrarySearch>) => void navigate({
    to: "/media",
    search: (previous) => ({ ...previous, ...patch, cursor: undefined, limit: patch.limit ?? previous.limit ?? 24, type: patch.type ?? previous.type ?? "all" }),
  });

  if (query.isError) {
    return <PageFrame breadcrumbs={[{ label: "Library" }, { label: "Media" }]}><QueryError error={query.error} retry={() => void query.refetch()} /></PageFrame>;
  }

  const items = query.data.items.filter((item) => search.type === "all" || mediaKind(item.artifact.mediaType) === search.type);
  return <PageFrame breadcrumbs={[{ label: "Library" }, { label: "Media" }]}>
    <PageHeader title="Media Library" description="Find the image or video you want by sight, then open it to edit or publish.">
      {canAdd && <Button type="button" aria-expanded={adding} onClick={() => setAdding((value) => !value)}>{adding ? "Close upload" : "Add media"}</Button>}
    </PageHeader>

    {adding && canAdd && <Card>
      <CardHeader><CardTitle>Add an image or video</CardTitle></CardHeader>
      <CardContent><MediaDropzone onUploaded={(media) => {
        void queryClient.invalidateQueries({ queryKey: ["controller", "media"] });
        void navigate({ to: "/media/$mediaId", params: { mediaId: media.id } });
      }} /></CardContent>
    </Card>}

    <form className="flex flex-col gap-3 rounded-lg border border-border bg-card p-4 sm:flex-row sm:items-end" role="search" onSubmit={(event) => {
      event.preventDefault();
      updateSearch({ search: searchDraft.trim() || undefined });
    }}>
      <label className="min-w-0 flex-1 text-sm font-medium">Search media
        <input className={fieldClass} type="search" value={searchDraft} onChange={(event) => setSearchDraft(event.currentTarget.value)} placeholder="Title, artist, or tag" />
      </label>
      <label className="text-sm font-medium">Type
        <select className={`${fieldClass} sm:w-40`} value={search.type} onChange={(event) => updateSearch({ type: event.currentTarget.value as MediaTypeFilter })}>
          <option value="all">Images and videos</option>
          <option value="image">Images</option>
          <option value="video">Videos</option>
        </select>
      </label>
      <Button type="submit" variant="secondary">Search</Button>
    </form>

    {items.length > 0 ? <>
      <PreviewMosaic label="Playable media" columns={4}>{items.map((media) => <MediaLibraryCard media={media} key={media.id} />)}</PreviewMosaic>
      <Pagination
        page={query.data.page}
        onPrevious={() => void navigate({ to: "/media", search: (previous) => ({ ...previous, cursor: query.data.page.previousCursor ?? undefined, limit: previous.limit ?? 24, type: previous.type ?? "all" }) })}
        onNext={() => void navigate({ to: "/media", search: (previous) => ({ ...previous, cursor: query.data.page.nextCursor ?? undefined, limit: previous.limit ?? 24, type: previous.type ?? "all" }) })}
      />
    </> : <EmptyState
      title={search.search || search.type !== "all" ? "No matching media" : "Build your visual library"}
      description={search.search || search.type !== "all" ? "Try a different title, artist, tag, or media type." : "Add an image or video to make it available for playlists and displays."}
      action={canAdd && !adding ? <Button type="button" onClick={() => setAdding(true)}>Add your first media</Button> : undefined}
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
    ...media.playback,
  };
}

function MediaEditor({ media, etag }: { media: Media; etag: string }) {
  const queryClient = useQueryClient();
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
    },
  });
  const set = <K extends keyof MediaFormState>(key: K, value: MediaFormState[K]) => setForm((current) => ({ ...current, [key]: value }));

  return <Card>
    <CardHeader><CardTitle>Media details</CardTitle></CardHeader>
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
        <fieldset className="space-y-4 rounded-md border border-border p-4">
          <legend className="px-1 text-sm font-semibold">Playback defaults</legend>
          <div className="grid gap-4 sm:grid-cols-3">
            <label className="text-sm font-medium">Fit<select className={fieldClass} value={form.fit} onChange={(event) => set("fit", event.currentTarget.value as "fit" | "fill")}><option value="fit">Fit entire frame</option><option value="fill">Fill frame</option></select></label>
            <label className="text-sm font-medium">Orientation<select className={fieldClass} value={form.orientation} onChange={(event) => set("orientation", Number(event.currentTarget.value) as MediaFormState["orientation"])}><option value={0}>0°</option><option value={90}>90°</option><option value={180}>180°</option><option value={270}>270°</option></select></label>
            <label className="text-sm font-medium">Volume<input className={fieldClass} type="number" min={0} max={100} step={1} disabled={form.muted} value={form.volume} onChange={(event) => set("volume", Number(event.currentTarget.value))} /></label>
          </div>
          <div className="flex flex-wrap gap-5">
            <label className="inline-flex items-center gap-2 text-sm font-medium"><input type="checkbox" checked={form.loop} onChange={(event) => set("loop", event.currentTarget.checked)} />Loop playback</label>
            <label className="inline-flex items-center gap-2 text-sm font-medium"><input type="checkbox" checked={form.muted} onChange={(event) => set("muted", event.currentTarget.checked)} />Muted</label>
          </div>
        </fieldset>
        {mutation.isError && <ErrorState title={mutation.error instanceof ApiRequestError && mutation.error.status === 412 ? "Media changed elsewhere" : "Could not save changes"} description={mutation.error.message} />}
        {mutation.isSuccess && <p role="status" className="text-sm font-medium text-status-success">Changes saved.</p>}
        <div className="flex flex-wrap gap-2"><Button type="submit" disabled={mutation.isPending || !form.title.trim()}>{mutation.isPending ? "Saving…" : "Save changes"}</Button><Button type="button" variant="secondary" disabled={mutation.isPending} onClick={() => setForm(formFromMedia(media))}>Reset</Button></div>
      </form>
    </CardContent>
  </Card>;
}

function UsageSummary({ usage, pending, error, retry }: { usage?: MediaUsage; pending: boolean; error?: Error; retry: () => void }) {
  if (pending) return <Card><CardContent className="space-y-3 pt-5"><Skeleton className="h-5 w-36" /><Skeleton className="h-12 w-full" /></CardContent></Card>;
  if (error) return <QueryError error={error} retry={retry} />;
  return <Card>
    <CardHeader><CardTitle>Used in</CardTitle></CardHeader>
    <CardContent className="grid grid-cols-2 gap-4">
      <div><p className="text-2xl font-semibold tabular-nums">{usage?.scheduleCount ?? 0}</p><p className="text-sm text-muted-foreground">Schedules</p></div>
      <div><p className="text-2xl font-semibold tabular-nums">{usage?.releaseCount ?? 0}</p><p className="text-sm text-muted-foreground">Releases</p></div>
    </CardContent>
  </Card>;
}

export function MediaDetailPage() {
  const { mediaId = "" } = useParams({ strict: false });
  const auth = useAuth();
  const mediaQuery = useQuery({ queryKey: ["controller", "media-detail", mediaId], queryFn: () => getMedia(mediaId) });
  const usageQuery = useQuery({ queryKey: ["controller", "media-usage", mediaId], queryFn: () => getMediaUsage(mediaId), enabled: Boolean(mediaId) });
  const [dimensions, setDimensions] = React.useState<PixelDimensions>();
  const [assigning, setAssigning] = React.useState(false);
  if (mediaQuery.isPending) return <LibraryPending />;
  if (mediaQuery.isError) return <PageFrame breadcrumbs={[{ label: "Library", href: "/media" }, { label: "Media" }]}><QueryError error={mediaQuery.error} retry={() => void mediaQuery.refetch()} /></PageFrame>;

  const { media, etag } = mediaQuery.data;
  const canEdit = Boolean(auth.operator && hasCapability(auth.operator, "media_write", "media_write"));
  return <PageFrame breadcrumbs={[{ label: "Library" }, { label: "Media", href: "/media" }, { label: media.title }]}>
    <PageHeader title={media.title} description={media.artist || media.description}>
      <Button type="button" onClick={() => setAssigning((value) => !value)}>{assigning ? "Close assignment" : "Assign"}</Button>
    </PageHeader>
    {assigning && <NodeAssignmentComposer fixedAsset={{ id: media.assetId, name: media.title, playback: media.playback }} title="Assign media" />}

    <AuthenticatedMediaPreview media={media} hero onDimensions={setDimensions} />

    <div className="grid gap-6 lg:grid-cols-3">
      <div className="lg:col-span-2">{canEdit ? <MediaEditor media={media} etag={etag} /> : <Card><CardHeader><CardTitle>Media details</CardTitle></CardHeader><CardContent className="space-y-3"><p className="text-sm text-muted-foreground">You can view this media, but your operator permissions do not allow editing.</p>{media.description && <p>{media.description}</p>}{media.tags.length > 0 && <div className="flex flex-wrap gap-2">{media.tags.map((tag) => <span className="rounded-full bg-muted px-2 py-1 text-xs" key={tag}>{tag}</span>)}</div>}</CardContent></Card>}</div>
      <div className="space-y-6">
        <UsageSummary usage={usageQuery.data} pending={usageQuery.isPending} error={usageQuery.error ?? undefined} retry={() => void usageQuery.refetch()} />
        <TechnicalDetails items={[
          { label: "Asset", value: media.assetId },
          { label: "Digest", value: <span className="font-mono text-xs">{media.artifact.digest}</span> },
          { label: "MIME type", value: media.artifact.mediaType },
          { label: "Size", value: formatBytes(media.artifact.size) },
          ...(dimensions ? [{ label: "Pixels", value: `${dimensions.width.toLocaleString()} × ${dimensions.height.toLocaleString()} px` }] : []),
          { label: "Version", value: media.version.toLocaleString() },
        ]} />
      </div>
    </div>
  </PageFrame>;
}
