import * as React from "react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import type { Asset, components } from "@cable/api-client";
import {
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  DataTable,
  EmptyState,
  ErrorState,
  MediaCard,
  PageFrame as SharedPageFrame,
  PageHeader,
  PreviewMosaic,
  PreviewSurface,
  Skeleton,
  StatusBadge,
  TechnicalDetails,
  useToast,
} from "@cable/controller-ui";
import { ApiRequestError, api, readApiResult } from "./api";
import { AuthoringMediaPreview } from "./components/AuthoringMediaPreview";
import { MediaPicker } from "./components/MediaPicker";
import { DragHandleIcon, SortableList } from "./components/SortableList";

type Playlist = components["schemas"]["Playlist"];
type PlaylistItem = components["schemas"]["PlaylistItem"];
type PlaylistItemInput = components["schemas"]["PlaylistItemInput"];
type Media = components["schemas"]["Media"];
type MediaPage = components["schemas"]["MediaPage"];
type UsagePage = components["schemas"]["UsagePage"];

type LinkIntent = { href: string; label: string };
export type AuthoringResourceKind = "playlist";

export interface UsageReference {
  kind: string;
  id: string;
  name: string;
  href: string;
}

export interface AuthoringVisualRequest {
  kind: AuthoringResourceKind;
  id: string;
  /** Exact legacy asset references on the resource. Implementations must not scan the media catalog. */
  assetIds: readonly string[];
}

export interface AuthoringVisualSnapshot {
  /** A bounded page containing only media explicitly resolved for this resource. */
  media?: MediaPage;
  usage?: UsagePage;
}

export interface AuthoringQuerySeam {
  query(request: AuthoringVisualRequest): Promise<AuthoringVisualSnapshot>;
}

const AuthoringQueryContext = React.createContext<AuthoringQuerySeam | null>(null);

export function AuthoringQueryProvider({ seam, children }: { seam: AuthoringQuerySeam; children: React.ReactNode }) {
  return <AuthoringQueryContext.Provider value={seam}>{children}</AuthoringQueryContext.Provider>;
}

/** Stable links shared by authoring cards and by callers composing these pages. */
export const authoringLinks = {
  asset: (id: string): LinkIntent => ({ href: `/library/assets/${encodeURIComponent(id)}`, label: "View asset" }),
  playlist: (id: string): LinkIntent => ({ href: `/library/playlists/${encodeURIComponent(id)}`, label: "View playlist" }),
  release: (id: string): LinkIntent => ({ href: `/releases/${encodeURIComponent(id)}`, label: "View release" }),
};

export function formatTimestamp(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? "Unknown time" : date.toLocaleString();
}

export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "Unknown duration";
  const rounded = Math.round(seconds);
  const hours = Math.floor(rounded / 3_600);
  const minutes = Math.floor((rounded % 3_600) / 60);
  const remainder = rounded % 60;
  if (hours) return `${hours}h ${minutes}m`;
  return minutes ? `${minutes}m ${remainder}s` : `${remainder}s`;
}

async function getAsset(assetId: string): Promise<Asset> {
  return readApiResult(await api.GET("/assets/{assetId}", { params: { path: { assetId } } }));
}
async function listAssets(): Promise<{ items: Asset[] }> {
  return readApiResult(await api.GET("/assets", { params: { query: { limit: 100 } } }));
}
async function getPlaylist(playlistId: string): Promise<Playlist> {
  return readApiResult(await api.GET("/playlists/{playlistId}", { params: { path: { playlistId } } }));
}
async function listPlaylists(): Promise<{ items: Playlist[] }> {
  return readApiResult(await api.GET("/playlists", { params: { query: { limit: 100 } } }));
}
/** Re-submits items in order with exactly one source each: the media row when known, else the file asset. */
function replacementItems(items: PlaylistItem[]): PlaylistItemInput[] {
  return [...items].sort((left, right) => left.position - right.position).map((item, index) => ({
    ...(item.mediaId ? { mediaId: item.mediaId } : { assetId: item.assetId }),
    position: index + 1,
    durationSeconds: item.durationSeconds,
    title: item.title || undefined,
    subtitle: item.subtitle || undefined,
  }));
}
export function authoringIfMatch(version: number): string {
  return `"v${version}"`;
}
function mutationMessage(error: unknown, fallback: string): string {
  if (error instanceof ApiRequestError && error.status === 412) return "This resource changed elsewhere. Reload before saving.";
  return error instanceof Error ? error.message : fallback;
}

function useAuthoringVisuals(request: AuthoringVisualRequest | null, override?: AuthoringQuerySeam) {
  const inherited = React.useContext(AuthoringQueryContext);
  const seam = override ?? inherited;
  const assetKey = request?.assetIds.join("\u001f") ?? "";
  return useQuery({
    queryKey: ["authoring", "visuals", request?.kind, request?.id, assetKey],
    queryFn: () => seam!.query(request!),
    enabled: Boolean(seam && request),
    staleTime: 30_000,
  });
}

function QueryBoundary({ query, children }: { query: { isPending: boolean; isError: boolean; error: unknown; refetch: () => unknown }; children: React.ReactNode }) {
  if (query.isPending) return <SharedPageFrame><div className="space-y-4" aria-busy="true"><Skeleton className="h-7 w-48" /><Skeleton className="h-40 w-full" /></div></SharedPageFrame>;
  if (query.isError) return <SharedPageFrame><ErrorState title="Unable to load authoring data" description={query.error instanceof Error ? query.error.message : "The controller returned an unexpected error."} onRetry={() => void query.refetch()} /></SharedPageFrame>;
  return <>{children}</>;
}

function PageFrame({ title, description, children }: { title: string; description?: string; children: React.ReactNode }) {
  return <SharedPageFrame><PageHeader title={title} description={description} />{children}</SharedPageFrame>;
}

function EmptyPreview({ label, description = "Media details have not been resolved for this item." }: { label: string; description?: string }) {
  return <PreviewSurface aria-label={label} state="empty" emptyTitle="Preview not resolved" emptyDescription={description} />;
}

function mediaMap(snapshot?: AuthoringVisualSnapshot): Map<string, Media> {
  return new Map((snapshot?.media?.items ?? []).flatMap((media) => media.assetId ? [[media.assetId, media] as const] : []));
}

function extractUsageReferences(usage?: UsagePage): UsageReference[] {
  return usage?.references ?? [];
}

function visualQueryNote(query: { isPending: boolean; isError: boolean; data?: AuthoringVisualSnapshot } | undefined) {
  if (!query) return "Visual enrichment is not connected on this route.";
  if (query.isPending) return "Resolving media and usage…";
  if (query.isError) return "Media and usage details could not be resolved.";
  return null;
}

function PlaylistItemPreview({ item, media, compact = false }: { item: PlaylistItem; media?: Media; compact?: boolean }) {
  return media
    ? <AuthoringMediaPreview media={media} label={`${item.title || media.title} preview`} compact={compact} />
    : <EmptyPreview label={`${item.title || "Playlist item"} preview`} />;
}

export function AssetAuthoringListPage() {
  const query = useQuery({ queryKey: ["authoring", "assets"], queryFn: listAssets });
  return <QueryBoundary query={query}><PageFrame title="Media assets" description="Immutable media artifacts available to playlists and screens."><Card><CardHeader><CardTitle>Asset library</CardTitle></CardHeader><CardContent><DataTable caption="Media assets" rows={query.data?.items ?? []} rowKey={(asset) => asset.id} empty={<EmptyState title="No assets" description="No media assets have been registered yet." />} columns={[{ key: "name", header: "Asset", render: (asset) => <a className="font-medium text-primary hover:underline" href={authoringLinks.asset(asset.id).href}>{asset.name}</a> }, { key: "type", header: "Type", render: (asset) => asset.artifact.mediaType }, { key: "state", header: "State", render: (asset) => <StatusBadge tone={asset.artifact.state === "ready" ? "success" : "warning"}>{asset.artifact.state ?? "Unknown"}</StatusBadge> }]} /></CardContent></Card></PageFrame></QueryBoundary>;
}

export function AssetAuthoringDetailPage({ assetId }: { assetId: string }) {
  const query = useQuery({ queryKey: ["authoring", "asset", assetId], queryFn: () => getAsset(assetId) });
  return <QueryBoundary query={query}>{query.data && <AssetDetail asset={query.data} />}</QueryBoundary>;
}

function AssetDetail({ asset }: { asset: Asset }) {
  return <PageFrame title={asset.name} description="Media artifact provenance and authoring usage."><div className="grid gap-4 lg:grid-cols-2"><Card><CardHeader><CardTitle>Artifact provenance</CardTitle></CardHeader><CardContent><dl className="grid gap-4 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">Digest</dt><dd className="break-all font-mono">{asset.artifact.digest}</dd></div><div><dt className="text-muted-foreground">Media type</dt><dd>{asset.artifact.mediaType}</dd></div><div><dt className="text-muted-foreground">Size</dt><dd>{asset.artifact.size.toLocaleString()} bytes</dd></div><div><dt className="text-muted-foreground">State</dt><dd>{asset.artifact.state ?? "Unknown"}</dd></div></dl></CardContent></Card><Card><CardHeader><CardTitle>Usage</CardTitle></CardHeader><CardContent><p className="text-sm text-muted-foreground">Asset membership is shown from each playlist detail. No client-only media records are inferred.</p><a className="mt-3 inline-block text-sm text-primary hover:underline" href="/library/playlists">Browse playlists</a></CardContent></Card></div><TechnicalDetails items={[{ label: "Asset ID", value: asset.id }]} /></PageFrame>;
}

export function PlaylistAuthoringListPage() {
  const query = useQuery({ queryKey: ["authoring", "playlists"], queryFn: listPlaylists });
  const playlists = query.data?.items ?? [];
  const assetIds = [...new Set(playlists.flatMap((playlist) => playlist.items.flatMap((item) => item.assetId ? [item.assetId] : [])))].sort();
  const mediaQuery = useQuery({
    queryKey: ["authoring", "playlist-library-media", assetIds],
    queryFn: async () => readApiResult(await api.GET("/media", {
      params: { query: { assetId: assetIds, limit: 100, playable: true } },
    })),
    enabled: assetIds.length > 0,
  });
  const resolved = new Map((mediaQuery.data?.items ?? []).flatMap((media) => media.assetId ? [[media.assetId, media] as const] : []));

  return <QueryBoundary query={query}>
    <PageFrame title="Playlists" description="Ordered media ready to assign to a screen.">
      {playlists.length
        ? <PreviewMosaic label="Playlists" columns={3}>
            {playlists.map((playlist) => {
              const first = playlist.items[0];
              const media = first ? resolved.get(first.assetId ?? "") : undefined;
              return <MediaCard
                key={playlist.id}
                title={playlist.name}
                description={playlist.description || "No description"}
                titleHref={authoringLinks.playlist(playlist.id).href}
                preview={media
                  ? <AuthoringMediaPreview media={media} label={`${playlist.name} preview`} />
                  : <EmptyPreview label={`${playlist.name} mosaic`} description={playlist.items.length ? "Resolving the first item preview…" : "This playlist does not contain media yet."} />}
                metadata={<><span>{playlist.items.length} {playlist.items.length === 1 ? "item" : "items"}</span><span>{formatDuration(playlist.items.reduce((sum, item) => sum + item.durationSeconds, 0))}</span></>}
              />;
            })}
          </PreviewMosaic>
        : <EmptyState title="No playlists" description="Create a playlist to group authored media into a repeatable sequence." />}
    </PageFrame>
  </QueryBoundary>;
}

export interface PlaylistInspectorProps {
  playlist: Playlist;
  visuals?: AuthoringVisualSnapshot;
  visualState?: string | null;
  mutating?: boolean;
  onReorder?: (orderedItemIds: string[]) => void;
  onAdd?: (media: Media[], durationSeconds: number) => void;
  onRemove?: (itemId: string) => void;
  onDurationChange?: (itemId: string, durationSeconds: number) => void;
  /** Media referenced by playlist items without a file asset (web pages), keyed by media id. */
  webMedia?: ReadonlyMap<string, Media>;
}

/** Web pages play on desktop, browser, and Android screens; appliance players reject releases that contain them. */
export const webPlaylistHint = "Web pages play on desktop, browser, and Android screens only. Appliance players cannot show a playlist that contains them.";

export function PlaylistInspector({ playlist, visuals, visualState, mutating = false, onReorder, onAdd, onRemove, onDurationChange, webMedia }: PlaylistInspectorProps) {
  const ordered = [...playlist.items].sort((left, right) => left.position - right.position);
  const resolved = mediaMap(visuals);
  const totalSeconds = ordered.reduce((sum, item) => sum + item.durationSeconds, 0);
  const [newDuration, setNewDuration] = React.useState(60);
  const [picking, setPicking] = React.useState(false);
  const itemMedia = (item: PlaylistItem) => item.assetId ? resolved.get(item.assetId) : webMedia?.get(item.mediaId ?? "");
  const itemName = (item: PlaylistItem) => item.title || itemMedia(item)?.title || "Untitled media";
  const webMediaIds = new Set(playlist.items.flatMap((item) => item.mediaId && !item.assetId ? [item.mediaId] : []));
  return <div className="space-y-6">
    <div className="flex flex-wrap items-center justify-between gap-3">
      <div><h2 id="playlist-order-heading" className="text-lg font-semibold tracking-tight">Play order</h2><p className="mt-1 text-sm text-muted-foreground">{ordered.length} {ordered.length === 1 ? "item" : "items"} · {formatDuration(totalSeconds)} total runtime</p></div>
      {onAdd && <Button type="button" aria-haspopup="dialog" disabled={mutating} onClick={() => setPicking(true)}>Add media</Button>}
    </div>
    {visualState && <p className="text-xs text-muted-foreground">{visualState}</p>}
    {webMediaIds.size > 0 && <p role="note" className="rounded-md border border-status-warning/40 px-3 py-2 text-sm text-muted-foreground">{webPlaylistHint}</p>}
    {onAdd && picking && <MediaPicker
      open={picking}
      onOpenChange={setPicking}
      title="Add media to playlist"
      description={`Select one or more items. Ctrl+Enter adds the selection. ${webPlaylistHint}`}
      excludeAssetIds={playlist.items.flatMap((item) => item.assetId ? [item.assetId] : [])}
      kinds={["image", "video", "web"]}
      filter={(media) => !webMediaIds.has(media.id)}
      onConfirm={(media) => onAdd(media, newDuration)}
      footerStart={<label className="inline-flex items-center gap-2 text-foreground">Seconds each<input aria-label="Duration for added media (seconds)" className="h-8 w-20 rounded-md border border-border bg-background px-2" type="number" min={1} max={86400} value={newDuration} onChange={(event) => setNewDuration(Math.min(86400, Math.max(1, Number(event.currentTarget.value) || 1)))} /></label>}
    />}
    <section aria-labelledby="playlist-order-heading">
      {ordered.length ? <SortableList
        label="Playlist order"
        className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-card"
        itemClassName="grid grid-cols-[auto_5.5rem_minmax(0,1fr)] items-center gap-x-3 gap-y-2 p-3 sm:grid-cols-[auto_2rem_8rem_minmax(0,1fr)_auto]"
        items={ordered}
        itemKey={(item) => item.id}
        itemLabel={itemName}
        disabled={!onReorder || mutating || ordered.length < 2}
        onReorder={(next) => onReorder?.(next.map((item) => item.id))}
        renderItem={(item, { index, handleProps }) => {
          const media = itemMedia(item);
          const name = itemName(item);
          return <>
            {onReorder ? <button {...handleProps} className="inline-flex h-9 w-9 cursor-grab touch-none items-center justify-center self-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring active:cursor-grabbing disabled:cursor-not-allowed disabled:opacity-40 aria-pressed:bg-primary aria-pressed:text-primary-foreground"><DragHandleIcon /></button> : <span />}
            <span className="hidden text-sm font-semibold tabular-nums text-muted-foreground sm:inline" aria-label={`Position ${index + 1}`}>{index + 1}</span>
            <div><PlaylistItemPreview item={item} media={media} compact /></div>
            <div className="min-w-0"><h3 className="truncate font-medium">{name}</h3>{item.subtitle && <p className="truncate text-sm text-muted-foreground">{item.subtitle}</p>}<label className="mt-1 inline-flex items-center gap-2 text-xs text-muted-foreground">Duration<input key={`${item.id}:${item.durationSeconds}`} aria-label={`Duration for ${name}`} className="h-8 w-20 rounded-md border border-border bg-background px-2 text-foreground" type="number" min={1} max={86400} defaultValue={item.durationSeconds} onKeyDown={(event) => { if (event.key === "Enter") event.currentTarget.blur(); }} onBlur={(event) => { const value = Math.min(86400, Math.max(1, Number(event.currentTarget.value) || 1)); if (value !== item.durationSeconds) onDurationChange?.(item.id, value); }} disabled={mutating || !onDurationChange} /><span>seconds</span></label></div>
            {onRemove && <div className="col-start-3 flex justify-end sm:col-start-auto"><Button type="button" variant="secondary" size="sm" aria-label={`Remove ${name}`} disabled={mutating} onClick={() => onRemove(item.id)}>Remove</Button></div>}
          </>;
        }}
      /> : <EmptyState title="Playlist is empty" description="Add media to start building a play order." />}
    </section>
    <TechnicalDetails items={[{ label: "Playlist ID", value: playlist.id }, { label: "Revision", value: `v${playlist.version}` }, { label: "Created by", value: playlist.createdBy }, { label: "Created", value: formatTimestamp(playlist.createdAt) }, { label: "Updated", value: formatTimestamp(playlist.updatedAt) }]} />
  </div>;
}

export function PlaylistAuthoringDetailPage({ playlistId, querySeam }: { playlistId: string; querySeam?: AuthoringQuerySeam }) {
  const queryClient = useQueryClient();
  const query = useQuery({ queryKey: ["authoring", "playlist", playlistId], queryFn: () => getPlaylist(playlistId) });
  const [optimistic, setOptimistic] = React.useState<Playlist | null>(null);
  const request = query.data ? { kind: "playlist" as const, id: playlistId, assetIds: query.data.items.flatMap((item) => item.assetId ? [item.assetId] : []) } : null;
  const visualQuery = useAuthoringVisuals(request, querySeam);
  const webMediaIds = [...new Set((query.data?.items ?? []).flatMap((item) => item.mediaId && !item.assetId ? [item.mediaId] : []))];
  const webMediaQueries = useQueries({ queries: webMediaIds.map((mediaId) => ({ queryKey: ["controller", "media-detail-lookup", mediaId], queryFn: async () => readApiResult(await api.GET("/media/{mediaId}", { params: { path: { mediaId } } })), staleTime: 30_000 })) });
  const webMedia = new Map(webMediaQueries.flatMap((result) => result.data ? [[result.data.id, result.data] as const] : []));
  const inheritedSeam = React.useContext(AuthoringQueryContext);
  const [saving, setSaving] = React.useState(false);
  const [mutationError, setMutationError] = React.useState<string | null>(null);
  const toast = useToast();
  async function replace(items: PlaylistItemInput[], message: string, preview?: Playlist) {
    if (!query.data) return;
    setMutationError(null); setSaving(true); setOptimistic(preview ?? null);
    try {
      const updated = readApiResult(await api.PATCH("/playlists/{playlistId}", { params: { path: { playlistId }, header: { "If-Match": authoringIfMatch(query.data.version) } }, body: { name: query.data.name, description: query.data.description, items } }));
      queryClient.setQueryData(["authoring", "playlist", playlistId], updated);
      await queryClient.invalidateQueries({ queryKey: ["authoring", "playlist", playlistId] });
      await queryClient.invalidateQueries({ queryKey: ["authoring", "playlists"] });
      toast.success(message);
    } catch (error) { setMutationError(mutationMessage(error, "The playlist could not be saved.")); } finally { setSaving(false); setOptimistic(null); }
  }
  async function reorder(orderedItemIds: string[]) {
    if (!query.data) return;
    const byId = new Map(query.data.items.map((item) => [item.id, item]));
    const ordered = orderedItemIds.flatMap((id) => byId.get(id) ?? []).map((item, index) => ({ ...item, position: index + 1 }));
    if (ordered.length !== query.data.items.length) return;
    await replace(replacementItems(ordered), "Playlist order saved.", { ...query.data, items: ordered });
  }
  async function add(media: Media[], durationSeconds: number) {
    if (!query.data || !media.length) return;
    const start = query.data.items.length;
    await replace([...replacementItems(query.data.items), ...media.map((selected, index) => ({ mediaId: selected.id, position: start + index + 1, durationSeconds, title: selected.title || undefined }))], media.length === 1 ? "Media added to playlist." : `${media.length} items added to playlist.`);
  }
  async function remove(itemId: string) {
    if (!query.data) return;
    await replace(replacementItems(query.data.items.filter((item) => item.id !== itemId)), "Media removed from playlist.");
  }
  async function updateDuration(itemId: string, durationSeconds: number) {
    if (!query.data) return;
    await replace(replacementItems(query.data.items.map((item) => item.id === itemId ? { ...item, durationSeconds } : item)), "Duration updated.");
  }
  return <QueryBoundary query={query}>{query.data && <PageFrame title={query.data.name} description={query.data.description || "An ordered set of media for scheduled playback."}>{mutationError && <ErrorState title="Unable to save playlist" description={mutationError} />}<PlaylistInspector playlist={optimistic ?? query.data} visuals={visualQuery.data} visualState={visualQueryNote(querySeam || inheritedSeam ? visualQuery : undefined)} mutating={saving || query.isFetching} onReorder={reorder} onAdd={add} onRemove={remove} onDurationChange={updateDuration} webMedia={webMedia} /></PageFrame>}</QueryBoundary>;
}
