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
  StatusSummary,
  StatusBadge,
  TechnicalDetails,
  useToast,
} from "@cable/controller-ui";
import { ApiRequestError, api, readApiResult } from "./api";
import { AuthoringMediaPreview } from "./components/AuthoringMediaPreview";
import { MediaField, MediaPicker } from "./components/MediaPicker";
import { DragHandleIcon, SortableList } from "./components/SortableList";
import { ChannelTabs } from "./cable";

type CableChannel = components["schemas"]["CableChannel"];
type CablePlaylist = components["schemas"]["CablePlaylist"];
type CablePlaylistItem = components["schemas"]["CablePlaylistItem"];
type CablePlaylistItemInput = components["schemas"]["CablePlaylistItemInput"];
type CableBlock = components["schemas"]["CableBlock"];
type CableBlockInput = components["schemas"]["CableBlockInput"];
type CableChannelInput = components["schemas"]["CableChannelInput"];
type Media = components["schemas"]["Media"];
type MediaPage = components["schemas"]["MediaPage"];
type UsagePage = components["schemas"]["UsagePage"];
type CurrentContent = components["schemas"]["CurrentContent"];

type LinkIntent = { href: string; label: string };
export type AuthoringResourceKind = "playlist" | "block" | "channel";

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
  currentContent?: CurrentContent[];
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
  block: (id: string): LinkIntent => ({ href: `/programming/blocks/${encodeURIComponent(id)}`, label: "View block" }),
  channel: (id: string): LinkIntent => ({ href: `/programming/channels/${encodeURIComponent(id)}`, label: "View channel" }),
  deployment: (id: string): LinkIntent => ({ href: `/programming/deployments/${encodeURIComponent(id)}`, label: "View deployment" }),
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

function secondsBetween(startsAt: string, endsAt: string): number {
  const start = new Date(startsAt).valueOf();
  const end = new Date(endsAt).valueOf();
  return Number.isFinite(start) && Number.isFinite(end) && end >= start ? (end - start) / 1_000 : Number.NaN;
}

async function getAsset(assetId: string): Promise<Asset> {
  return readApiResult(await api.GET("/assets/{assetId}", { params: { path: { assetId } } }));
}
async function listAssets(): Promise<{ items: Asset[] }> {
  return readApiResult(await api.GET("/assets", { params: { query: { limit: 100 } } }));
}
async function getPlaylist(playlistId: string): Promise<CablePlaylist> {
  return readApiResult(await api.GET("/cable/playlists/{playlistId}", { params: { path: { playlistId } } }));
}
async function listPlaylists(): Promise<{ items: CablePlaylist[] }> {
  return readApiResult(await api.GET("/cable/playlists", { params: { query: { limit: 100 } } }));
}
async function getChannel(channelId: string): Promise<CableChannel> {
  return readApiResult(await api.GET("/cable/channels/{channelId}", { params: { path: { channelId } } }));
}
async function listChannels(deploymentId: string): Promise<{ items: CableChannel[] }> {
  return readApiResult(await api.GET("/cable/deployments/{deploymentId}/channels", { params: { path: { deploymentId }, query: { limit: 100 } } }));
}
async function listBlocks(channelId: string): Promise<{ items: CableBlock[] }> {
  return readApiResult(await api.GET("/cable/channels/{channelId}/blocks", { params: { path: { channelId }, query: { limit: 100 } } }));
}
function localDateTime(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.valueOf())) return "";
  const pad = (number: number) => String(number).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}
function isoDateTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? value : date.toISOString();
}
function replacementItems(items: CablePlaylistItem[]): CablePlaylistItemInput[] {
  return [...items].sort((left, right) => left.position - right.position).map((item, index) => ({
    assetId: item.assetId,
    mediaId: item.mediaId,
    position: index + 1,
    durationSeconds: item.durationSeconds,
    title: item.title || undefined,
    subtitle: item.subtitle || undefined,
  }));
}
async function getBlock(blockId: string): Promise<CableBlock> {
  return readApiResult(await api.GET("/cable/blocks/{blockId}", { params: { path: { blockId } } }));
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

function InlineQueryBoundary({ query, children }: { query: { isPending: boolean; isError: boolean; error: unknown; refetch: () => unknown }; children: React.ReactNode }) {
  if (query.isPending) return <div className="space-y-3" aria-busy="true"><Skeleton className="h-6 w-40" /><Skeleton className="h-40 w-full" /></div>;
  if (query.isError) return <ErrorState title="Unable to load related authoring data" description={query.error instanceof Error ? query.error.message : "The controller returned an unexpected error."} onRetry={() => void query.refetch()} />;
  return <>{children}</>;
}

function PageFrame({ title, description, tabs, children }: { title: string; description?: string; tabs?: React.ReactNode; children: React.ReactNode }) {
  return <SharedPageFrame><PageHeader title={title} description={description} />{tabs}{children}</SharedPageFrame>;
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

function SourceLabel({ block }: { block: CableBlock }) {
  if (block.playlistId) return <>Playlist source</>;
  if (block.assetId) return <>Single media source</>;
  return <>Source unavailable</>;
}

function BlockWindow({ block }: { block: CableBlock }) {
  const duration = secondsBetween(block.startsAt, block.endsAt);
  return <dl className="grid gap-3 text-sm sm:grid-cols-3">
    <div><dt className="text-xs text-muted-foreground">Starts</dt><dd><time dateTime={block.startsAt}>{formatTimestamp(block.startsAt)}</time></dd></div>
    <div><dt className="text-xs text-muted-foreground">Ends</dt><dd><time dateTime={block.endsAt}>{formatTimestamp(block.endsAt)}</time></dd></div>
    <div><dt className="text-xs text-muted-foreground">Runtime</dt><dd className="tabular-nums">{formatDuration(duration)}</dd></div>
  </dl>;
}

function PlaylistItemPreview({ item, media, compact = false }: { item: CablePlaylistItem; media?: Media; compact?: boolean }) {
  return media
    ? <AuthoringMediaPreview media={media} label={`${item.title || media.title} preview`} compact={compact} />
    : <EmptyPreview label={`${item.title || "Playlist item"} preview`} />;
}

export function AssetAuthoringListPage() {
  const query = useQuery({ queryKey: ["authoring", "assets"], queryFn: listAssets });
  return <QueryBoundary query={query}><PageFrame title="Media assets" description="Immutable media artifacts available to Cable programming."><Card><CardHeader><CardTitle>Asset library</CardTitle></CardHeader><CardContent><DataTable caption="Media assets" rows={query.data?.items ?? []} rowKey={(asset) => asset.id} empty={<EmptyState title="No assets" description="No media assets have been registered yet." />} columns={[{ key: "name", header: "Asset", render: (asset) => <a className="font-medium text-primary hover:underline" href={authoringLinks.asset(asset.id).href}>{asset.name}</a> }, { key: "type", header: "Type", render: (asset) => asset.artifact.mediaType }, { key: "state", header: "State", render: (asset) => <StatusBadge tone={asset.artifact.state === "ready" ? "success" : "warning"}>{asset.artifact.state ?? "Unknown"}</StatusBadge> }]} /></CardContent></Card></PageFrame></QueryBoundary>;
}

export function AssetAuthoringDetailPage({ assetId }: { assetId: string }) {
  const query = useQuery({ queryKey: ["authoring", "asset", assetId], queryFn: () => getAsset(assetId) });
  return <QueryBoundary query={query}>{query.data && <AssetDetail asset={query.data} />}</QueryBoundary>;
}

function AssetDetail({ asset }: { asset: Asset }) {
  return <PageFrame title={asset.name} description="Media artifact provenance and authoring usage."><div className="grid gap-4 lg:grid-cols-2"><Card><CardHeader><CardTitle>Artifact provenance</CardTitle></CardHeader><CardContent><dl className="grid gap-4 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">Digest</dt><dd className="break-all font-mono">{asset.artifact.digest}</dd></div><div><dt className="text-muted-foreground">Media type</dt><dd>{asset.artifact.mediaType}</dd></div><div><dt className="text-muted-foreground">Size</dt><dd>{asset.artifact.size.toLocaleString()} bytes</dd></div><div><dt className="text-muted-foreground">State</dt><dd>{asset.artifact.state ?? "Unknown"}</dd></div></dl></CardContent></Card><Card><CardHeader><CardTitle>Usage</CardTitle></CardHeader><CardContent><p className="text-sm text-muted-foreground">Asset membership is shown from each playlist or block detail. No client-only media records are inferred.</p><a className="mt-3 inline-block text-sm text-primary hover:underline" href="/library/playlists">Browse playlists</a></CardContent></Card></div><TechnicalDetails items={[{ label: "Asset ID", value: asset.id }]} /></PageFrame>;
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
    <PageFrame title="Playlists" description="Ordered programs ready to place in a channel block.">
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
  playlist: CablePlaylist;
  visuals?: AuthoringVisualSnapshot;
  visualState?: string | null;
  mutating?: boolean;
  onReorder?: (orderedItemIds: string[]) => void;
  onAdd?: (media: Media[], durationSeconds: number) => void;
  onRemove?: (itemId: string) => void;
  onDurationChange?: (itemId: string, durationSeconds: number) => void;
  /** Web media referenced by playlist items (`mediaId`), keyed by media id. */
  webMedia?: ReadonlyMap<string, Media>;
}

/** Web pages play on desktop, browser, and Android screens; appliance players reject releases that contain them. */
export const webPlaylistHint = "Web pages play on desktop, browser, and Android screens only. Appliance players cannot show a playlist that contains them, and the Cable guide skips them.";

export function PlaylistInspector({ playlist, visuals, visualState, mutating = false, onReorder, onAdd, onRemove, onDurationChange, webMedia }: PlaylistInspectorProps) {
  const ordered = [...playlist.items].sort((left, right) => left.position - right.position);
  const resolved = mediaMap(visuals);
  const totalSeconds = ordered.reduce((sum, item) => sum + item.durationSeconds, 0);
  const [newDuration, setNewDuration] = React.useState(60);
  const [picking, setPicking] = React.useState(false);
  const itemMedia = (item: CablePlaylistItem) => item.mediaId ? webMedia?.get(item.mediaId) : resolved.get(item.assetId ?? "");
  const itemName = (item: CablePlaylistItem) => item.title || itemMedia(item)?.title || "Untitled media";
  const webMediaIds = new Set(playlist.items.flatMap((item) => item.mediaId ? [item.mediaId] : []));
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
      filter={(media) => !media.deploymentId && !webMediaIds.has(media.id)}
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
  const [optimistic, setOptimistic] = React.useState<CablePlaylist | null>(null);
  const request = query.data ? { kind: "playlist" as const, id: playlistId, assetIds: query.data.items.flatMap((item) => item.assetId ? [item.assetId] : []) } : null;
  const visualQuery = useAuthoringVisuals(request, querySeam);
  const webMediaIds = [...new Set((query.data?.items ?? []).flatMap((item) => item.mediaId ? [item.mediaId] : []))];
  const webMediaQueries = useQueries({ queries: webMediaIds.map((mediaId) => ({ queryKey: ["controller", "media-detail-lookup", mediaId], queryFn: async () => readApiResult(await api.GET("/media/{mediaId}", { params: { path: { mediaId } } })), staleTime: 30_000 })) });
  const webMedia = new Map(webMediaQueries.flatMap((result) => result.data ? [[result.data.id, result.data] as const] : []));
  const inheritedSeam = React.useContext(AuthoringQueryContext);
  const [saving, setSaving] = React.useState(false);
  const [mutationError, setMutationError] = React.useState<string | null>(null);
  const toast = useToast();
  async function replace(items: CablePlaylistItemInput[], message: string, preview?: CablePlaylist) {
    if (!query.data) return;
    setMutationError(null); setSaving(true); setOptimistic(preview ?? null);
    try {
      const updated = readApiResult(await api.PATCH("/cable/playlists/{playlistId}", { params: { path: { playlistId }, header: { "If-Match": authoringIfMatch(query.data.version) } }, body: { name: query.data.name, description: query.data.description, items } }));
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
    await replace([...replacementItems(query.data.items), ...media.map((selected, index) => ({ ...(selected.kind === "web" ? { mediaId: selected.id } : { assetId: selected.assetId }), position: start + index + 1, durationSeconds, title: selected.title || undefined }))], media.length === 1 ? "Media added to playlist." : `${media.length} items added to playlist.`);
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

export function ChannelAuthoringListPage({ deploymentId }: { deploymentId: string }) {
  const query = useQuery({ queryKey: ["authoring", "channels", deploymentId], queryFn: () => listChannels(deploymentId) });
  const channels = query.data?.items ?? [];
  return <QueryBoundary query={query}><PageFrame title="Channels" description="Live programming identities and their next scheduled moves.">{channels.length ? <PreviewMosaic label="Channels" columns={3}>{channels.map((channel) => <MediaCard key={channel.id} title={`${channel.number} · ${channel.name}`} artist={channel.callsign || undefined} description={channel.description || "No channel description"} titleHref={authoringLinks.channel(channel.id).href} preview={<EmptyPreview label={`${channel.name} channel artwork`} description={channel.artworkAssetId ? "Artwork media has not been resolved on this route." : "No channel artwork is assigned."} />} status={channel.enabled ? channel.standby ? "standby" : "active" : "disabled"} />)}</PreviewMosaic> : <EmptyState title="No channels" description="This deployment has no channels yet." />}</PageFrame></QueryBoundary>;
}

export interface ChannelInspectorProps {
  channel: CableChannel;
  blocks: CableBlock[];
  visuals?: AuthoringVisualSnapshot;
  visualState?: string | null;
  now?: Date;
  action?: React.ReactNode;
}

export function ChannelInspector({ channel, blocks, visuals, visualState, now = new Date(), action }: ChannelInspectorProps) {
  const ordered = [...blocks].sort((left, right) => new Date(left.startsAt).valueOf() - new Date(right.startsAt).valueOf());
  const nowValue = now.valueOf();
  const current = ordered.find((block) => new Date(block.startsAt).valueOf() <= nowValue && new Date(block.endsAt).valueOf() > nowValue);
  const next = ordered.find((block) => new Date(block.startsAt).valueOf() > nowValue);
  const media = mediaMap(visuals).get(channel.artworkAssetId ?? "");
  const observations = visuals?.currentContent?.filter((content) => content.channelId === channel.id) ?? [];
  const timelineDuration = ordered.reduce((sum, block) => sum + Math.max(secondsBetween(block.startsAt, block.endsAt), 0), 0);
  return <div className="space-y-6">
    <div className="grid gap-4 lg:grid-cols-[minmax(0,2fr)_minmax(16rem,1fr)]">
      <MediaCard title={`${channel.number} · ${channel.name}`} artist={channel.callsign || undefined} description={channel.description || "No channel description"} preview={media ? <AuthoringMediaPreview media={media} label={`${channel.name} artwork`} /> : <EmptyPreview label={`${channel.name} artwork`} description={channel.artworkAssetId ? "Artwork media has not been resolved." : "No channel artwork is assigned."} />} status={channel.enabled ? channel.standby ? "standby" : "active" : "disabled"} actions={action} />
      <Card><CardHeader><CardTitle>Schedule status</CardTitle></CardHeader><CardContent className="space-y-3"><StatusSummary status={current ? "scheduled" : "unscheduled"} label={current ? "Scheduled now" : "No current block"} detail={current?.title} /><p className="text-sm text-muted-foreground">{observations.length ? `${observations.length} current player ${observations.length === 1 ? "observation" : "observations"}.` : "No player observation is available for this channel."}</p>{visualState && <p className="text-xs text-muted-foreground">{visualState}</p>}</CardContent></Card>
    </div>
    <a className="inline-block max-w-full break-words text-sm font-medium text-primary hover:underline" href={authoringLinks.channel(channel.id).href + "/blocks"}>Open full block schedule</a>
    <section aria-labelledby="current-next-heading" className="space-y-3"><h2 id="current-next-heading" className="text-lg font-semibold tracking-tight">Current and next</h2><div className="grid gap-4 md:grid-cols-2">{current ? <MediaCard title={current.title} description={`${formatTimestamp(current.startsAt)} – ${formatTimestamp(current.endsAt)}`} titleHref={authoringLinks.block(current.id).href} preview={<EmptyPreview label={`${current.title} source preview`} description="Open the block to resolve its authored sequence." />} status="playing" statusLabel="Current" metadata={<span><SourceLabel block={current} /></span>} /> : <EmptyState title="Nothing scheduled now" description="The authored schedule has no block covering the current time." />}{next ? <MediaCard title={next.title} description={`Starts ${formatTimestamp(next.startsAt)}`} titleHref={authoringLinks.block(next.id).href} preview={<EmptyPreview label={`${next.title} source preview`} description="Open the block to resolve its authored sequence." />} status="pending" statusLabel="Up next" metadata={<span><SourceLabel block={next} /></span>} /> : <EmptyState title="No next block" description="There is no later authored block in the loaded schedule window." />}</div></section>
    <section aria-labelledby="timeline-heading" className="space-y-3"><div><h2 id="timeline-heading" className="text-lg font-semibold tracking-tight">Schedule timeline</h2><p className="text-sm text-muted-foreground">Block sizes are proportional to runtime in the loaded window.</p></div>{ordered.length ? <ol className="flex min-h-24 flex-col overflow-hidden rounded-lg border border-border bg-card sm:flex-row" aria-label="Proportional channel schedule">{ordered.map((block) => { const duration = Math.max(secondsBetween(block.startsAt, block.endsAt), 0); return <li key={block.id} className="min-w-0 border-b border-border bg-muted/40 p-3 last:border-b-0 sm:border-b-0 sm:border-r sm:last:border-r-0" style={{ flexBasis: 0, flexGrow: timelineDuration ? duration / timelineDuration : 1 }}><a className="block truncate text-sm font-medium hover:text-primary hover:underline" href={authoringLinks.block(block.id).href}>{block.title}</a><time className="mt-1 block truncate text-xs text-muted-foreground" dateTime={block.startsAt}>{new Date(block.startsAt).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}</time></li>; })}</ol> : <EmptyState title="No scheduled blocks" description="This channel has no authored blocks in the loaded schedule window." />}</section>
    <TechnicalDetails items={[{ label: "Channel ID", value: channel.id }, { label: "Deployment ID", value: channel.deploymentId }, { label: "Revision", value: `v${channel.version}` }, { label: "Artwork asset ID", value: channel.artworkAssetId || "Not assigned" }, { label: "Created", value: formatTimestamp(channel.createdAt) }, { label: "Updated", value: formatTimestamp(channel.updatedAt) }]} />
  </div>;
}

function ChannelEditForm({ draft, saving, error, onChange, onSave, onCancel }: { draft: CableChannelInput; saving: boolean; error: string | null; onChange: (draft: CableChannelInput) => void; onSave: () => void; onCancel: () => void }) {
  return <Card><CardHeader><CardTitle>Edit channel</CardTitle></CardHeader><CardContent><form className="grid gap-4 sm:grid-cols-2" onSubmit={(event) => { event.preventDefault(); onSave(); }}>
    <label className="text-sm font-medium">Name<input required className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.name} onChange={(event) => onChange({ ...draft, name: event.currentTarget.value })} /></label>
    <label className="text-sm font-medium">Callsign<input className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.callsign ?? ""} onChange={(event) => onChange({ ...draft, callsign: event.currentTarget.value })} /></label>
    <label className="text-sm font-medium sm:col-span-2">Description<textarea className="mt-1 min-h-24 w-full rounded-md border border-border bg-background p-3" value={draft.description ?? ""} onChange={(event) => onChange({ ...draft, description: event.currentTarget.value })} /></label>
    <MediaField label="Artwork" noneLabel="No artwork" kinds={["image"]} value={draft.artworkAssetId} onChange={(media) => onChange({ ...draft, artworkAssetId: media?.assetId })} disabled={saving} />
    <label className="flex items-center gap-2 self-end pb-2 text-sm font-medium"><input type="checkbox" checked={draft.enabled ?? false} onChange={(event) => onChange({ ...draft, enabled: event.currentTarget.checked })} />Enabled</label>
    <label className="flex items-center gap-2 text-sm font-medium"><input type="checkbox" checked={draft.standby ?? false} onChange={(event) => onChange({ ...draft, standby: event.currentTarget.checked })} />Standby</label>
    {error && <p role="alert" className="text-sm text-status-danger sm:col-span-2">{error}</p>}
    <div className="flex flex-wrap gap-2 sm:col-span-2"><Button type="submit" disabled={saving}>{saving ? "Saving…" : "Save channel"}</Button><Button type="button" variant="secondary" disabled={saving} onClick={onCancel}>Cancel</Button></div>
  </form></CardContent></Card>;
}

export function ChannelAuthoringDetailPage({ channelId, querySeam }: { channelId: string; querySeam?: AuthoringQuerySeam }) {
  const queryClient = useQueryClient();
  const query = useQuery({ queryKey: ["authoring", "channel", channelId], queryFn: () => getChannel(channelId) });
  const blocks = useQuery({ queryKey: ["authoring", "channel", channelId, "blocks"], queryFn: () => listBlocks(channelId), enabled: query.isSuccess });
  const assetIds = query.data?.artworkAssetId ? [query.data.artworkAssetId] : [];
  const visualQuery = useAuthoringVisuals(query.data ? { kind: "channel", id: channelId, assetIds } : null, querySeam);
  const inheritedSeam = React.useContext(AuthoringQueryContext);
  const [editing, setEditing] = React.useState(false);
  const [draft, setDraft] = React.useState<CableChannelInput | null>(null);
  const [saving, setSaving] = React.useState(false);
  const [saveError, setSaveError] = React.useState<string | null>(null);
  React.useEffect(() => { if (query.data) setDraft({ number: query.data.number, name: query.data.name, callsign: query.data.callsign, description: query.data.description, accent: query.data.accent, artworkAssetId: query.data.artworkAssetId, enabled: query.data.enabled, standby: query.data.standby }); }, [query.data]);
  async function save() {
    if (!query.data || !draft) return;
    setSaving(true); setSaveError(null);
    try {
      const result = readApiResult(await api.PATCH("/cable/channels/{channelId}", { params: { path: { channelId }, header: { "If-Match": authoringIfMatch(query.data.version) } }, body: draft }));
      queryClient.setQueryData(["authoring", "channel", channelId], result);
      await queryClient.invalidateQueries({ queryKey: ["authoring", "channel", channelId] });
      await queryClient.invalidateQueries({ queryKey: ["authoring", "channels"] });
      setEditing(false);
    } catch (error) { setSaveError(mutationMessage(error, "The channel could not be saved.")); }
    finally { setSaving(false); }
  }
  return <QueryBoundary query={query}>{query.data && <PageFrame title={`${query.data.number} · ${query.data.name}`} description="See the on-air identity, current block, and what follows." tabs={<ChannelTabs channelId={query.data.id} active="" />}>{editing && draft ? <ChannelEditForm draft={draft} saving={saving} error={saveError} onChange={setDraft} onSave={() => void save()} onCancel={() => setEditing(false)} /> : <InlineQueryBoundary query={blocks}>{blocks.data && <ChannelInspector channel={query.data} blocks={blocks.data.items} visuals={visualQuery.data} visualState={visualQueryNote(querySeam || inheritedSeam ? visualQuery : undefined)} action={<Button type="button" variant="secondary" size="sm" onClick={() => setEditing(true)}>Edit channel</Button>} />}</InlineQueryBoundary>}</PageFrame>}</QueryBoundary>;
}
export function BlockAuthoringListPage({ channelId }: { channelId: string }) {
  const query = useQuery({ queryKey: ["authoring", "blocks", channelId], queryFn: () => listBlocks(channelId) });
  const blocks = query.data?.items ?? [];
  return <QueryBoundary query={query}><PageFrame title="Blocks" description="Timed source windows in this channel's schedule." tabs={<ChannelTabs channelId={channelId} active="blocks" />}>{blocks.length ? <PreviewMosaic label="Schedule blocks" columns={3}>{blocks.map((block) => <MediaCard key={block.id} title={block.title} description={`${formatTimestamp(block.startsAt)} – ${formatTimestamp(block.endsAt)}`} titleHref={authoringLinks.block(block.id).href} preview={<EmptyPreview label={`${block.title} source preview`} description="Open the block to resolve its media sequence." />} metadata={<><span><SourceLabel block={block} /></span><span>{formatDuration(secondsBetween(block.startsAt, block.endsAt))}</span></>} />)}</PreviewMosaic> : <EmptyState title="No blocks" description="No schedule blocks have been authored for this channel yet." />}</PageFrame></QueryBoundary>;
}
export interface BlockInspectorProps {
  block: CableBlock;
  playlist?: CablePlaylist;
  visuals?: AuthoringVisualSnapshot;
  visualState?: string | null;
  sourceError?: boolean;
  action?: React.ReactNode;
}

export function BlockInspector({ block, playlist, visuals, visualState, sourceError = false, action }: BlockInspectorProps) {
  const resolved = mediaMap(visuals);
  const sourceAssetId = block.assetId ?? playlist?.items[0]?.assetId;
  const sourceMedia = sourceAssetId ? resolved.get(sourceAssetId) : undefined;
  const authoredItems = playlist ? [...playlist.items].sort((left, right) => left.position - right.position) : [];
  return <div className="space-y-6">
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)]"><MediaCard title={playlist?.name || sourceMedia?.title || block.title} description={playlist?.description || sourceMedia?.description || (block.playlistId ? "Playlist source" : "Single media source")} titleHref={block.playlistId ? authoringLinks.playlist(block.playlistId).href : block.assetId ? authoringLinks.asset(block.assetId).href : undefined} preview={sourceMedia ? <AuthoringMediaPreview media={sourceMedia} label={`${block.title} source preview`} /> : <EmptyPreview label={`${block.title} source preview`} description={sourceError ? "The authored source could not be loaded." : "The source media has not been resolved."} />} metadata={<span><SourceLabel block={block} /></span>} actions={action} /><Card><CardHeader><CardTitle>Schedule window</CardTitle></CardHeader><CardContent><BlockWindow block={block} /></CardContent></Card></div>
    {playlist && <section aria-labelledby="block-source-heading" className="space-y-3"><div><h2 id="block-source-heading" className="text-lg font-semibold tracking-tight">Authored source</h2><p className="text-sm text-muted-foreground">Playlist with {authoredItems.length} authored {authoredItems.length === 1 ? "item" : "items"}.</p></div><PreviewMosaic label={`${playlist.name} authored sequence`} columns={4}>{authoredItems.slice(0, 4).map((item) => <MediaCard key={item.id} title={item.title || resolved.get(item.assetId ?? "")?.title || "Untitled media"} description={item.subtitle || undefined} preview={<PlaylistItemPreview item={item} media={resolved.get(item.assetId ?? "")} />} metadata={<span>{formatDuration(item.durationSeconds)}</span>} />)}</PreviewMosaic></section>}
    <TechnicalDetails items={[{ label: "Block ID", value: block.id }, { label: "Channel ID", value: block.channelId }, { label: "Playlist ID", value: block.playlistId || "Not used" }, { label: "Asset ID", value: block.assetId || "Not used" }, { label: "Revision", value: `v${block.version}` }, { label: "Created by", value: block.createdBy }, { label: "Created", value: formatTimestamp(block.createdAt) }]} />
  </div>;
}

function BlockEditForm({ draft, playlists, saving, error, onChange, onSave, onCancel }: { draft: CableBlockInput & { sourceType: "playlist" | "media" }; playlists: CablePlaylist[]; saving: boolean; error: string | null; onChange: (draft: CableBlockInput & { sourceType: "playlist" | "media" }) => void; onSave: () => void; onCancel: () => void }) {
  return <Card><CardHeader><CardTitle>Edit block</CardTitle></CardHeader><CardContent><form className="grid gap-4 sm:grid-cols-2" onSubmit={(event) => { event.preventDefault(); onSave(); }}>
    <label className="text-sm font-medium sm:col-span-2">Title<input required className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.title ?? ""} onChange={(event) => onChange({ ...draft, title: event.currentTarget.value })} /></label>
    <label className="text-sm font-medium">Starts<input required type="datetime-local" className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.startsAt} onChange={(event) => onChange({ ...draft, startsAt: event.currentTarget.value })} /></label>
    <label className="text-sm font-medium">Ends<input required type="datetime-local" className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.endsAt} onChange={(event) => onChange({ ...draft, endsAt: event.currentTarget.value })} /></label>
    <label className="text-sm font-medium">Source type<select className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.sourceType} onChange={(event) => { const sourceType = event.currentTarget.value as "playlist" | "media"; onChange({ ...draft, sourceType, playlistId: sourceType === "playlist" ? (playlists[0]?.id || draft.playlistId) : undefined, assetId: sourceType === "media" ? draft.assetId : undefined }); }} disabled={saving}><option value="playlist">Playlist</option><option value="media">Single Media</option></select></label>
    {draft.sourceType === "playlist" && <label className="text-sm font-medium">Playlist<select required className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.playlistId ?? ""} onChange={(event) => onChange({ ...draft, playlistId: event.currentTarget.value || undefined })} disabled={saving}><option value="">Choose playlist</option>{playlists.map((playlist) => <option key={playlist.id} value={playlist.id}>{playlist.name}</option>)}</select></label>}
    {draft.sourceType === "media" && <MediaField label="Media" kinds={["image", "video"]} value={draft.assetId} onChange={(media) => onChange({ ...draft, assetId: media?.assetId })} disabled={saving} />}
    <p className="text-xs text-muted-foreground sm:col-span-2">Blocks feed the Cable guide, which plays images and videos only. Web pages are skipped, including any inside the chosen playlist.</p>
    {error && <p role="alert" className="text-sm text-status-danger sm:col-span-2">{error}</p>}
    <div className="flex flex-wrap gap-2 sm:col-span-2"><Button type="submit" disabled={saving || (draft.sourceType === "playlist" && !draft.playlistId) || (draft.sourceType === "media" && !draft.assetId)}>{saving ? "Saving…" : "Save block"}</Button><Button type="button" variant="secondary" disabled={saving} onClick={onCancel}>Cancel</Button></div>
  </form></CardContent></Card>;
}

export function BlockAuthoringDetailPage({ blockId, querySeam }: { blockId: string; querySeam?: AuthoringQuerySeam }) {
  const queryClient = useQueryClient();
  const query = useQuery({ queryKey: ["authoring", "block", blockId], queryFn: () => getBlock(blockId) });
  const playlist = useQuery({ queryKey: ["authoring", "block", blockId, "playlist", query.data?.playlistId], queryFn: () => getPlaylist(query.data!.playlistId!), enabled: Boolean(query.data?.playlistId) });
  const playlists = useQuery({ queryKey: ["authoring", "playlists"], queryFn: listPlaylists });
  const assetIds = query.data ? [query.data.assetId, ...(playlist.data?.items.map((item) => item.assetId) ?? [])].filter((id): id is string => Boolean(id)) : [];
  const visualQuery = useAuthoringVisuals(query.data ? { kind: "block", id: blockId, assetIds } : null, querySeam);
  const inheritedSeam = React.useContext(AuthoringQueryContext);
  const [editing, setEditing] = React.useState(false);
  const [draft, setDraft] = React.useState<(CableBlockInput & { sourceType: "playlist" | "media" }) | null>(null);
  const [saving, setSaving] = React.useState(false);
  const [saveError, setSaveError] = React.useState<string | null>(null);
  React.useEffect(() => { if (query.data) setDraft({ playlistId: query.data.playlistId, assetId: query.data.assetId, startsAt: localDateTime(query.data.startsAt), endsAt: localDateTime(query.data.endsAt), title: query.data.title, sourceType: query.data.playlistId ? "playlist" : "media" }); }, [query.data]);
  async function save() {
    if (!query.data || !draft) return;
    if (new Date(draft.endsAt).valueOf() <= new Date(draft.startsAt).valueOf()) { setSaveError("End time must be after start time."); return; }
    setSaving(true); setSaveError(null);
    try {
      const body: CableBlockInput = { startsAt: isoDateTime(draft.startsAt), endsAt: isoDateTime(draft.endsAt), title: draft.title || undefined, playlistId: draft.sourceType === "playlist" ? draft.playlistId : undefined, assetId: draft.sourceType === "media" ? draft.assetId : undefined };
      const result = readApiResult(await api.PATCH("/cable/blocks/{blockId}", { params: { path: { blockId }, header: { "If-Match": authoringIfMatch(query.data.version) } }, body }));
      queryClient.setQueryData(["authoring", "block", blockId], result);
      await queryClient.invalidateQueries({ queryKey: ["authoring", "block", blockId] });
      await queryClient.invalidateQueries({ queryKey: ["authoring", "blocks"] });
      setEditing(false);
    } catch (error) { setSaveError(mutationMessage(error, "The block could not be saved.")); }
    finally { setSaving(false); }
  }
  return <QueryBoundary query={query}>{query.data && <PageFrame title={query.data.title} description="Inspect the authored source and schedule window.">{editing && draft ? <BlockEditForm draft={draft} playlists={playlists.data?.items ?? []} saving={saving} error={saveError} onChange={setDraft} onSave={() => void save()} onCancel={() => setEditing(false)} /> : <BlockInspector block={query.data} playlist={playlist.data} visuals={visualQuery.data} visualState={visualQueryNote(querySeam || inheritedSeam ? visualQuery : undefined)} sourceError={playlist.isError} action={<Button type="button" variant="secondary" size="sm" onClick={() => setEditing(true)}>Edit block</Button>} />}</PageFrame>}</QueryBoundary>;
}
