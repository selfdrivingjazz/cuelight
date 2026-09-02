import * as React from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
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
  PreviewMosaic,
  PreviewSurface,
  Skeleton,
  StatusSummary,
  StatusBadge,
  TechnicalDetails,
} from "@cable/controller-ui";
import { ApiRequestError, api, readApiResult } from "./api";
import { AuthoringMediaPreview } from "./components/AuthoringMediaPreview";

type CableChannel = components["schemas"]["CableChannel"];
type CablePlaylist = components["schemas"]["CablePlaylist"];
type CablePlaylistItem = components["schemas"]["CablePlaylistItem"];
type CablePlaylistItemInput = components["schemas"]["CablePlaylistItemInput"];
type CableBlock = components["schemas"]["CableBlock"];
type CableBlockInput = components["schemas"]["CableBlockInput"];
type CableProfile = components["schemas"]["CableProfile"];
type CableProfileInput = components["schemas"]["CableProfileInput"];
type CableChannelInput = components["schemas"]["CableChannelInput"];
type CableRevisionProgram = components["schemas"]["CableRevisionProgram"];
type Media = components["schemas"]["Media"];
type MediaPage = components["schemas"]["MediaPage"];
type UsagePage = components["schemas"]["UsagePage"];
type CurrentContent = components["schemas"]["CurrentContent"];
type CableShellBuild = components["schemas"]["CableShellBuild"];
type CableDeployment = components["schemas"]["CableDeployment"];
type ReleaseEnvelope = components["schemas"]["ReleaseEnvelope"];

type LinkIntent = { href: string; label: string };
export type AuthoringResourceKind = "playlist" | "block" | "channel" | "profile";

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
  compiledPrograms?: CableRevisionProgram[];
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
  asset: (id: string): LinkIntent => ({ href: `/assets/${encodeURIComponent(id)}`, label: "View asset" }),
  playlist: (id: string): LinkIntent => ({ href: `/cable/playlists/${encodeURIComponent(id)}`, label: "View playlist" }),
  block: (id: string): LinkIntent => ({ href: `/cable/blocks/${encodeURIComponent(id)}`, label: "View block" }),
  channel: (id: string): LinkIntent => ({ href: `/cable/channels/${encodeURIComponent(id)}`, label: "View channel" }),
  profile: (id: string): LinkIntent => ({ href: `/cable/profiles/${encodeURIComponent(id)}`, label: "View profile" }),
  deployment: (id: string): LinkIntent => ({ href: `/cable/deployments/${encodeURIComponent(id)}`, label: "View deployment" }),
  release: (id: string): LinkIntent => ({ href: `/cable/releases/${encodeURIComponent(id)}`, label: "View release" }),
  shellBuild: (id: string): LinkIntent => ({ href: `/cable/shell-builds/${encodeURIComponent(id)}`, label: "View shell build" }),
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
async function getProfile(profileId: string): Promise<CableProfile> {
  return readApiResult(await api.GET("/cable/profiles/{profileId}", { params: { path: { profileId } } }));
}
async function listMedia(): Promise<MediaPage> {
  return readApiResult(await api.GET("/media", { params: { query: { limit: 100, playable: true } } }));
}
async function listShellBuilds(): Promise<{ items: CableShellBuild[] }> {
  return readApiResult(await api.GET("/cable/shell-builds", { params: { query: { limit: 100 } } })) as { items: CableShellBuild[] };
}
async function listReleases(): Promise<{ items: ReleaseEnvelope[] }> {
  return readApiResult(await api.GET("/releases", { params: { query: { limit: 100 } } })) as { items: ReleaseEnvelope[] };
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
    position: index + 1,
    durationSeconds: item.durationSeconds,
    title: item.title || undefined,
    subtitle: item.subtitle || undefined,
  }));
}
async function listProfiles(): Promise<{ items: CableProfile[] }> {
  return readApiResult(await api.GET("/cable/profiles", { params: { query: { limit: 100 } } }));
}
async function getBlock(blockId: string): Promise<CableBlock> {
  return readApiResult(await api.GET("/cable/blocks/{blockId}", { params: { path: { blockId } } }));
}
async function listDeployments(): Promise<{ items: CableDeployment[] }> {
  return readApiResult(await api.GET("/cable/deployments", { params: { query: { limit: 100 } } })) as { items: CableDeployment[] };
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
  if (query.isPending) return <main className="mx-auto max-w-[var(--content-max-width)] space-y-4 p-4 md:p-8" aria-busy="true"><Skeleton className="h-7 w-48" /><Skeleton className="h-40 w-full" /></main>;
  if (query.isError) return <main className="mx-auto max-w-[var(--content-max-width)] p-4 md:p-8"><ErrorState title="Unable to load authoring data" description={query.error instanceof Error ? query.error.message : "The controller returned an unexpected error."} onRetry={() => void query.refetch()} /></main>;
  return <>{children}</>;
}

function InlineQueryBoundary({ query, children }: { query: { isPending: boolean; isError: boolean; error: unknown; refetch: () => unknown }; children: React.ReactNode }) {
  if (query.isPending) return <div className="space-y-3" aria-busy="true"><Skeleton className="h-6 w-40" /><Skeleton className="h-40 w-full" /></div>;
  if (query.isError) return <ErrorState title="Unable to load related authoring data" description={query.error instanceof Error ? query.error.message : "The controller returned an unexpected error."} onRetry={() => void query.refetch()} />;
  return <>{children}</>;
}

function PageFrame({ title, description, children }: { title: string; description?: string; children: React.ReactNode }) {
  return <main className="mx-auto max-w-[var(--content-max-width)] space-y-6 p-4 md:p-8">
    <header className="max-w-3xl">
      <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
      {description && <p className="mt-2 text-sm leading-6 text-muted-foreground">{description}</p>}
    </header>
    {children}
  </main>;
}

function EmptyPreview({ label, description = "Media details have not been resolved for this item." }: { label: string; description?: string }) {
  return <PreviewSurface aria-label={label} state="empty" emptyTitle="Preview not resolved" emptyDescription={description} />;
}

function mediaMap(snapshot?: AuthoringVisualSnapshot): Map<string, Media> {
  return new Map((snapshot?.media?.items ?? []).map((media) => [media.assetId, media]));
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
  return <PageFrame title={asset.name} description={`Asset ${asset.id}`}><div className="grid gap-4 lg:grid-cols-2"><Card><CardHeader><CardTitle>Artifact provenance</CardTitle></CardHeader><CardContent><dl className="grid gap-4 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">Digest</dt><dd className="break-all font-mono">{asset.artifact.digest}</dd></div><div><dt className="text-muted-foreground">Media type</dt><dd>{asset.artifact.mediaType}</dd></div><div><dt className="text-muted-foreground">Size</dt><dd>{asset.artifact.size.toLocaleString()} bytes</dd></div><div><dt className="text-muted-foreground">State</dt><dd>{asset.artifact.state ?? "Unknown"}</dd></div></dl></CardContent></Card><Card><CardHeader><CardTitle>Usage</CardTitle></CardHeader><CardContent><p className="text-sm text-muted-foreground">Asset membership is shown from each playlist or block detail. No client-only media records are inferred.</p><a className="mt-3 inline-block text-sm text-primary hover:underline" href="/cable/playlists">Browse playlists</a></CardContent></Card></div></PageFrame>;
}

export function PlaylistAuthoringListPage() {
  const query = useQuery({ queryKey: ["authoring", "playlists"], queryFn: listPlaylists });
  const playlists = query.data?.items ?? [];
  const assetIds = [...new Set(playlists.flatMap((playlist) => playlist.items.map((item) => item.assetId)))].sort();
  const mediaQuery = useQuery({
    queryKey: ["authoring", "playlist-library-media", assetIds],
    queryFn: async () => readApiResult(await api.GET("/media", {
      params: { query: { assetId: assetIds, limit: 100, playable: true } },
    })),
    enabled: assetIds.length > 0,
  });
  const resolved = new Map((mediaQuery.data?.items ?? []).map((media) => [media.assetId, media]));

  return <QueryBoundary query={query}>
    <PageFrame title="Playlists" description="Ordered programs ready to place in a channel block.">
      {playlists.length
        ? <PreviewMosaic label="Playlists" columns={3}>
            {playlists.map((playlist) => {
              const first = playlist.items[0];
              const media = first ? resolved.get(first.assetId) : undefined;
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
  mediaOptions?: Media[];
  movingItemId?: string | null;
  mutating?: boolean;
  onMove?: (itemId: string, direction: -1 | 1) => void;
  onAdd?: (assetId: string, durationSeconds: number) => void;
  onRemove?: (itemId: string) => void;
  onDurationChange?: (itemId: string, durationSeconds: number) => void;
}

export function PlaylistInspector({ playlist, visuals, visualState, mediaOptions = [], movingItemId, mutating = false, onMove, onAdd, onRemove, onDurationChange }: PlaylistInspectorProps) {
  const ordered = [...playlist.items].sort((left, right) => left.position - right.position);
  const resolved = mediaMap(visuals);
  const totalSeconds = ordered.reduce((sum, item) => sum + item.durationSeconds, 0);
  const [newAssetId, setNewAssetId] = React.useState("");
  const [newDuration, setNewDuration] = React.useState(60);
  const available = mediaOptions.filter((media) => !playlist.items.some((item) => item.assetId === media.assetId));
  return <div className="space-y-6">
    <section aria-labelledby="playlist-look-heading" className="space-y-3">
      <div className="flex flex-col gap-1 sm:flex-row sm:items-end sm:justify-between">
        <div><h2 id="playlist-look-heading" className="text-lg font-semibold tracking-tight">At a glance</h2><p className="text-sm text-muted-foreground">{ordered.length} {ordered.length === 1 ? "item" : "items"} · {formatDuration(totalSeconds)} total runtime</p></div>
        {visualState && <p className="text-xs text-muted-foreground">{visualState}</p>}
      </div>
      {ordered.length ? <PreviewMosaic label={`${playlist.name} media mosaic`} columns={4}>{ordered.slice(0, 4).map((item) => {
        const media = resolved.get(item.assetId);
        return <MediaCard key={item.id} title={item.title || media?.title || "Untitled media"} description={item.subtitle || undefined} preview={<PlaylistItemPreview item={item} media={media} />} metadata={<span>{formatDuration(item.durationSeconds)}</span>} />;
      })}</PreviewMosaic> : <EmptyState title="Playlist is empty" description="Add authored media before placing this playlist in a scheduled block." />}
    </section>
    <section aria-labelledby="playlist-add-heading" className="space-y-3">
      <div><h2 id="playlist-add-heading" className="text-lg font-semibold tracking-tight">Add media</h2><p className="text-sm text-muted-foreground">Choose playable Media from the catalog; no asset IDs need to be copied.</p></div>
      {available.length ? <div className="flex flex-col gap-3 rounded-lg border border-border bg-card p-4 sm:flex-row sm:items-end">
        <label className="min-w-0 flex-1 text-sm font-medium">Media<select className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={newAssetId} onChange={(event) => setNewAssetId(event.currentTarget.value)} disabled={mutating}><option value="">Choose media</option>{available.map((media) => <option key={media.assetId} value={media.assetId}>{media.title || "Untitled media"}</option>)}</select></label>
        <label className="text-sm font-medium">Duration (seconds)<input className="mt-1 h-9 w-32 rounded-md border border-border bg-background px-3" type="number" min={1} max={86400} value={newDuration} onChange={(event) => setNewDuration(Math.max(1, Number(event.currentTarget.value) || 1))} disabled={mutating} /></label>
        {onAdd && <Button type="button" disabled={!newAssetId || mutating} onClick={() => { onAdd(newAssetId, newDuration); setNewAssetId(""); }}>Add media</Button>}
      </div> : <EmptyState title={mediaOptions.length ? "All available media is already included" : "No playable media available"} description={mediaOptions.length ? "Choose another catalog item after one is removed." : "Register playable Media before adding it to this playlist."} />}
    </section>
    {ordered.length > 0 && <section aria-labelledby="playlist-order-heading" className="space-y-3">
      <div><h2 id="playlist-order-heading" className="text-lg font-semibold tracking-tight">Play order</h2><p className="text-sm text-muted-foreground">Media runs from top to bottom.</p></div>
      <ol className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-card" aria-label="Playlist order">{ordered.map((item, index) => {
        const media = resolved.get(item.assetId);
        return <li className="grid gap-3 p-3 sm:grid-cols-[2rem_8rem_minmax(0,1fr)_auto] sm:items-center" key={item.id}>
          <span className="text-sm font-semibold tabular-nums text-muted-foreground" aria-label={`Position ${index + 1}`}>{index + 1}</span>
          <PlaylistItemPreview item={item} media={media} compact />
          <div className="min-w-0"><h3 className="truncate font-medium">{item.title || media?.title || "Untitled media"}</h3>{item.subtitle && <p className="truncate text-sm text-muted-foreground">{item.subtitle}</p>}<label className="mt-1 inline-flex items-center gap-2 text-xs text-muted-foreground">Duration<input aria-label={`Duration for ${item.title || media?.title || "playlist item"}`} className="h-8 w-20 rounded-md border border-border bg-background px-2 text-foreground" type="number" min={1} max={86400} defaultValue={item.durationSeconds} onBlur={(event) => onDurationChange?.(item.id, Math.max(1, Number(event.currentTarget.value) || 1))} disabled={mutating} /><span>seconds</span></label></div>
          <div className="flex flex-wrap gap-2" aria-label={`${item.title || "Playlist item"} controls`}>{onMove && <><Button type="button" variant="secondary" size="sm" disabled={index === 0 || Boolean(movingItemId) || mutating} onClick={() => onMove(item.id, -1)}>Move up</Button><Button type="button" variant="secondary" size="sm" disabled={index === ordered.length - 1 || Boolean(movingItemId) || mutating} onClick={() => onMove(item.id, 1)}>Move down</Button></>}{onRemove && <Button type="button" variant="secondary" size="sm" disabled={mutating} onClick={() => onRemove(item.id)}>Remove</Button>}</div>
        </li>;
      })}</ol>
    </section>}
    <TechnicalDetails items={[{ label: "Playlist ID", value: playlist.id }, { label: "Revision", value: `v${playlist.version}` }, { label: "Created by", value: playlist.createdBy }, { label: "Created", value: formatTimestamp(playlist.createdAt) }, { label: "Updated", value: formatTimestamp(playlist.updatedAt) }]} />
  </div>;
}

export function PlaylistAuthoringDetailPage({ playlistId, querySeam }: { playlistId: string; querySeam?: AuthoringQuerySeam }) {
  const queryClient = useQueryClient();
  const query = useQuery({ queryKey: ["authoring", "playlist", playlistId], queryFn: () => getPlaylist(playlistId) });
  const mediaQuery = useQuery({ queryKey: ["authoring", "media-library"], queryFn: listMedia });
  const request = query.data ? { kind: "playlist" as const, id: playlistId, assetIds: query.data.items.map((item) => item.assetId) } : null;
  const visualQuery = useAuthoringVisuals(request, querySeam);
  const inheritedSeam = React.useContext(AuthoringQueryContext);
  const [movingItemId, setMovingItemId] = React.useState<string | null>(null);
  const [mutationError, setMutationError] = React.useState<string | null>(null);
  const [success, setSuccess] = React.useState<string | null>(null);
  async function replace(items: CablePlaylistItemInput[], message: string) {
    if (!query.data) return;
    setMutationError(null); setSuccess(null);
    try {
      const updated = readApiResult(await api.PATCH("/cable/playlists/{playlistId}", { params: { path: { playlistId }, header: { "If-Match": authoringIfMatch(query.data.version) } }, body: { name: query.data.name, description: query.data.description, items } }));
      queryClient.setQueryData(["authoring", "playlist", playlistId], updated);
      await queryClient.invalidateQueries({ queryKey: ["authoring", "playlist", playlistId] });
      await queryClient.invalidateQueries({ queryKey: ["authoring", "playlists"] });
      setSuccess(message);
    } catch (error) { setMutationError(mutationMessage(error, "The playlist could not be saved.")); }
  }
  async function move(itemId: string, direction: -1 | 1) {
    if (!query.data) return;
    const ordered = [...query.data.items].sort((left, right) => left.position - right.position);
    const index = ordered.findIndex((item) => item.id === itemId);
    const destination = index + direction;
    if (index < 0 || destination < 0 || destination >= ordered.length) return;
    [ordered[index], ordered[destination]] = [ordered[destination]!, ordered[index]!];
    setMovingItemId(itemId);
    try { await replace(replacementItems(ordered), "Playlist order saved."); } finally { setMovingItemId(null); }
  }
  async function add(assetId: string, durationSeconds: number) {
    if (!query.data) return;
    const selected = mediaQuery.data?.items.find((media) => media.assetId === assetId);
    const position = query.data.items.length + 1;
    await replace([...replacementItems(query.data.items), { assetId, position, durationSeconds, title: selected?.title || undefined }], "Media added to playlist.");
  }
  async function remove(itemId: string) {
    if (!query.data) return;
    await replace(replacementItems(query.data.items.filter((item) => item.id !== itemId)), "Media removed from playlist.");
  }
  async function updateDuration(itemId: string, durationSeconds: number) {
    if (!query.data) return;
    await replace(replacementItems(query.data.items.map((item) => item.id === itemId ? { ...item, durationSeconds } : item)), "Duration updated.");
  }
  if (mediaQuery.isError) return <QueryBoundary query={query}>{query.data && <PageFrame title={query.data.name} description={query.data.description || "An ordered set of media for scheduled playback."}><ErrorState title="Media catalog unavailable" description={mediaQuery.error instanceof Error ? mediaQuery.error.message : "Playable Media could not be loaded."} /></PageFrame>}</QueryBoundary>;
  return <QueryBoundary query={query}>{query.data && <PageFrame title={query.data.name} description={query.data.description || "An ordered set of media for scheduled playback."}>{mutationError && <ErrorState title="Unable to save playlist" description={mutationError} />}{success && <p role="status" className="text-sm font-medium text-status-success">{success}</p>}<PlaylistInspector playlist={query.data} mediaOptions={mediaQuery.data?.items} visuals={visualQuery.data} visualState={visualQueryNote(querySeam || inheritedSeam ? visualQuery : undefined)} movingItemId={movingItemId} mutating={Boolean(movingItemId) || query.isFetching} onMove={move} onAdd={add} onRemove={remove} onDurationChange={updateDuration} /></PageFrame>}</QueryBoundary>;
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
    <a className="inline-block text-sm font-medium text-primary hover:underline" href={authoringLinks.channel(channel.id).href + "/blocks"}>Open full block schedule</a>
    <section aria-labelledby="current-next-heading" className="space-y-3"><h2 id="current-next-heading" className="text-lg font-semibold tracking-tight">Current and next</h2><div className="grid gap-4 md:grid-cols-2">{current ? <MediaCard title={current.title} description={`${formatTimestamp(current.startsAt)} – ${formatTimestamp(current.endsAt)}`} titleHref={authoringLinks.block(current.id).href} preview={<EmptyPreview label={`${current.title} source preview`} description="Open the block to resolve its authored sequence." />} status="playing" statusLabel="Current" metadata={<span><SourceLabel block={current} /></span>} /> : <EmptyState title="Nothing scheduled now" description="The authored schedule has no block covering the current time." />}{next ? <MediaCard title={next.title} description={`Starts ${formatTimestamp(next.startsAt)}`} titleHref={authoringLinks.block(next.id).href} preview={<EmptyPreview label={`${next.title} source preview`} description="Open the block to resolve its authored sequence." />} status="pending" statusLabel="Up next" metadata={<span><SourceLabel block={next} /></span>} /> : <EmptyState title="No next block" description="There is no later authored block in the loaded schedule window." />}</div></section>
    <section aria-labelledby="timeline-heading" className="space-y-3"><div><h2 id="timeline-heading" className="text-lg font-semibold tracking-tight">Schedule timeline</h2><p className="text-sm text-muted-foreground">Block sizes are proportional to runtime in the loaded window.</p></div>{ordered.length ? <ol className="flex min-h-24 flex-col overflow-hidden rounded-lg border border-border bg-card sm:flex-row" aria-label="Proportional channel schedule">{ordered.map((block) => { const duration = Math.max(secondsBetween(block.startsAt, block.endsAt), 0); return <li key={block.id} className="min-w-0 border-b border-border bg-muted/40 p-3 last:border-b-0 sm:border-b-0 sm:border-r sm:last:border-r-0" style={{ flexBasis: 0, flexGrow: timelineDuration ? duration / timelineDuration : 1 }}><a className="block truncate text-sm font-medium hover:text-primary hover:underline" href={authoringLinks.block(block.id).href}>{block.title}</a><time className="mt-1 block truncate text-xs text-muted-foreground" dateTime={block.startsAt}>{new Date(block.startsAt).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}</time></li>; })}</ol> : <EmptyState title="No scheduled blocks" description="This channel has no authored blocks in the loaded schedule window." />}</section>
    <TechnicalDetails items={[{ label: "Channel ID", value: channel.id }, { label: "Deployment ID", value: channel.deploymentId }, { label: "Revision", value: `v${channel.version}` }, { label: "Artwork asset ID", value: channel.artworkAssetId || "Not assigned" }, { label: "Created", value: formatTimestamp(channel.createdAt) }, { label: "Updated", value: formatTimestamp(channel.updatedAt) }]} />
  </div>;
}

function ChannelEditForm({ draft, mediaOptions, saving, error, onChange, onSave, onCancel }: { draft: CableChannelInput; mediaOptions: Media[]; saving: boolean; error: string | null; onChange: (draft: CableChannelInput) => void; onSave: () => void; onCancel: () => void }) {
  return <Card><CardHeader><CardTitle>Edit channel</CardTitle></CardHeader><CardContent><form className="grid gap-4 sm:grid-cols-2" onSubmit={(event) => { event.preventDefault(); onSave(); }}>
    <label className="text-sm font-medium">Name<input required className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.name} onChange={(event) => onChange({ ...draft, name: event.currentTarget.value })} /></label>
    <label className="text-sm font-medium">Callsign<input className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.callsign ?? ""} onChange={(event) => onChange({ ...draft, callsign: event.currentTarget.value })} /></label>
    <label className="text-sm font-medium sm:col-span-2">Description<textarea className="mt-1 min-h-24 w-full rounded-md border border-border bg-background p-3" value={draft.description ?? ""} onChange={(event) => onChange({ ...draft, description: event.currentTarget.value })} /></label>
    <label className="text-sm font-medium">Artwork<select className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.artworkAssetId ?? ""} onChange={(event) => onChange({ ...draft, artworkAssetId: event.currentTarget.value || undefined })} disabled={saving}><option value="">No artwork</option>{mediaOptions.map((media) => <option key={media.assetId} value={media.assetId}>{media.title || "Untitled media"}</option>)}</select></label>
    <label className="flex items-center gap-2 self-end pb-2 text-sm font-medium"><input type="checkbox" checked={draft.enabled ?? false} onChange={(event) => onChange({ ...draft, enabled: event.currentTarget.checked })} />Enabled</label>
    <label className="flex items-center gap-2 text-sm font-medium"><input type="checkbox" checked={draft.standby ?? false} onChange={(event) => onChange({ ...draft, standby: event.currentTarget.checked })} />Standby</label>
    {error && <p role="alert" className="text-sm text-status-danger sm:col-span-2">{error}</p>}
    <div className="flex gap-2 sm:col-span-2"><Button type="submit" disabled={saving}>{saving ? "Saving…" : "Save channel"}</Button><Button type="button" variant="secondary" onClick={onCancel}>Cancel</Button></div>
  </form></CardContent></Card>;
}

export function ChannelAuthoringDetailPage({ channelId, querySeam }: { channelId: string; querySeam?: AuthoringQuerySeam }) {
  const queryClient = useQueryClient();
  const query = useQuery({ queryKey: ["authoring", "channel", channelId], queryFn: () => getChannel(channelId) });
  const blocks = useQuery({ queryKey: ["authoring", "channel", channelId, "blocks"], queryFn: () => listBlocks(channelId), enabled: query.isSuccess });
  const mediaQuery = useQuery({ queryKey: ["authoring", "media-library"], queryFn: listMedia });
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
  return <QueryBoundary query={query}>{query.data && <PageFrame title={`${query.data.number} · ${query.data.name}`} description="See the on-air identity, current block, and what follows.">{editing && draft ? <ChannelEditForm draft={draft} mediaOptions={mediaQuery.data?.items ?? []} saving={saving} error={saveError} onChange={setDraft} onSave={() => void save()} onCancel={() => setEditing(false)} /> : <InlineQueryBoundary query={blocks}>{blocks.data && <ChannelInspector channel={query.data} blocks={blocks.data.items} visuals={visualQuery.data} visualState={visualQueryNote(querySeam || inheritedSeam ? visualQuery : undefined)} action={<Button type="button" variant="secondary" size="sm" onClick={() => setEditing(true)}>Edit channel</Button>} />}</InlineQueryBoundary>}</PageFrame>}</QueryBoundary>;
}
export function BlockAuthoringListPage({ channelId }: { channelId: string }) {
  const query = useQuery({ queryKey: ["authoring", "blocks", channelId], queryFn: () => listBlocks(channelId) });
  const blocks = query.data?.items ?? [];
  return <QueryBoundary query={query}><PageFrame title="Blocks" description="Timed source windows in this channel's schedule.">{blocks.length ? <PreviewMosaic label="Schedule blocks" columns={3}>{blocks.map((block) => <MediaCard key={block.id} title={block.title} description={`${formatTimestamp(block.startsAt)} – ${formatTimestamp(block.endsAt)}`} titleHref={authoringLinks.block(block.id).href} preview={<EmptyPreview label={`${block.title} source preview`} description="Open the block to resolve its media sequence." />} metadata={<><span><SourceLabel block={block} /></span><span>{formatDuration(secondsBetween(block.startsAt, block.endsAt))}</span></>} />)}</PreviewMosaic> : <EmptyState title="No blocks" description="No schedule blocks have been authored for this channel yet." />}</PageFrame></QueryBoundary>;
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
  const compiled = visuals?.compiledPrograms;
  const authoredItems = playlist ? [...playlist.items].sort((left, right) => left.position - right.position) : [];
  return <div className="space-y-6">
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)]"><MediaCard title={playlist?.name || sourceMedia?.title || block.title} description={playlist?.description || sourceMedia?.description || (block.playlistId ? "Playlist source" : "Single media source")} titleHref={block.playlistId ? authoringLinks.playlist(block.playlistId).href : block.assetId ? authoringLinks.asset(block.assetId).href : undefined} preview={sourceMedia ? <AuthoringMediaPreview media={sourceMedia} label={`${block.title} source preview`} /> : <EmptyPreview label={`${block.title} source preview`} description={sourceError ? "The authored source could not be loaded." : "The source media has not been resolved."} />} metadata={<span><SourceLabel block={block} /></span>} actions={action} /><Card><CardHeader><CardTitle>Schedule window</CardTitle></CardHeader><CardContent><BlockWindow block={block} /></CardContent></Card></div>
    <section aria-labelledby="block-source-heading" className="space-y-3"><div><h2 id="block-source-heading" className="text-lg font-semibold tracking-tight">Authored source</h2><p className="text-sm text-muted-foreground">{playlist ? `Playlist with ${authoredItems.length} authored ${authoredItems.length === 1 ? "item" : "items"}.` : block.assetId ? "One Media item plays in this window." : "No source is assigned."}</p></div>{playlist ? <PreviewMosaic label={`${playlist.name} authored sequence`} columns={4}>{authoredItems.slice(0, 4).map((item) => <MediaCard key={item.id} title={item.title || resolved.get(item.assetId)?.title || "Untitled media"} description={item.subtitle || undefined} preview={<PlaylistItemPreview item={item} media={resolved.get(item.assetId)} />} metadata={<span>{formatDuration(item.durationSeconds)}</span>} />)}</PreviewMosaic> : sourceMedia ? <MediaCard title={sourceMedia.title} description={sourceMedia.description || "Single Media source"} preview={<AuthoringMediaPreview media={sourceMedia} label={`${block.title} media`} />} /> : <EmptyPreview label={`${block.title} source`} />}</section>
    <section aria-labelledby="compiled-sequence-heading" className="space-y-3"><div><h2 id="compiled-sequence-heading" className="text-lg font-semibold tracking-tight">Compiled media sequence</h2><p className="text-sm text-muted-foreground">The exact order players receive after schedule compilation.</p></div>{compiled?.length ? <ol className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-card" aria-label="Compiled media sequence">{compiled.map((program, index) => { const media = resolved.get(program.assetId); return <li key={`${program.id}-${program.occurrence ?? index}`} className="grid gap-3 p-3 sm:grid-cols-[2rem_8rem_minmax(0,1fr)_auto] sm:items-center"><span className="text-sm font-semibold tabular-nums text-muted-foreground">{index + 1}</span><PlaylistItemPreview item={{ id: program.itemId || program.id, playlistId: block.playlistId || "", assetId: program.assetId, position: program.itemPosition || index + 1, durationSeconds: secondsBetween(program.startsAt, program.endsAt), title: program.title || "", subtitle: program.subtitle || "" }} media={media} compact /><div><p className="font-medium">{program.title || media?.title || "Untitled media"}</p><p className="text-xs text-muted-foreground">{formatTimestamp(program.startsAt)} – {formatTimestamp(program.endsAt)}</p></div><span className="text-sm tabular-nums text-muted-foreground">{formatDuration(secondsBetween(program.startsAt, program.endsAt))}</span></li>; })}</ol> : authoredItems.length ? <PreviewMosaic label="Authored fallback sequence" columns={4}>{authoredItems.slice(0, 4).map((item) => <MediaCard key={item.id} title={item.title || resolved.get(item.assetId)?.title || "Untitled media"} description="Showing the authored playlist order instead" preview={<PlaylistItemPreview item={item} media={resolved.get(item.assetId)} />} metadata={<span>{formatDuration(item.durationSeconds)}</span>} />)}</PreviewMosaic> : <EmptyState title="Compiled sequence unavailable" description={visualState || "The controller has not returned compiled programs for this block."} />}</section>
    <TechnicalDetails items={[{ label: "Block ID", value: block.id }, { label: "Channel ID", value: block.channelId }, { label: "Playlist ID", value: block.playlistId || "Not used" }, { label: "Asset ID", value: block.assetId || "Not used" }, { label: "Revision", value: `v${block.version}` }, { label: "Created by", value: block.createdBy }, { label: "Created", value: formatTimestamp(block.createdAt) }]} />
  </div>;
}

function BlockEditForm({ draft, playlists, mediaOptions, saving, error, onChange, onSave, onCancel }: { draft: CableBlockInput & { sourceType: "playlist" | "media" }; playlists: CablePlaylist[]; mediaOptions: Media[]; saving: boolean; error: string | null; onChange: (draft: CableBlockInput & { sourceType: "playlist" | "media" }) => void; onSave: () => void; onCancel: () => void }) {
  return <Card><CardHeader><CardTitle>Edit block</CardTitle></CardHeader><CardContent><form className="grid gap-4 sm:grid-cols-2" onSubmit={(event) => { event.preventDefault(); onSave(); }}>
    <label className="text-sm font-medium sm:col-span-2">Title<input required className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.title ?? ""} onChange={(event) => onChange({ ...draft, title: event.currentTarget.value })} /></label>
    <label className="text-sm font-medium">Starts<input required type="datetime-local" className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.startsAt} onChange={(event) => onChange({ ...draft, startsAt: event.currentTarget.value })} /></label>
    <label className="text-sm font-medium">Ends<input required type="datetime-local" className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.endsAt} onChange={(event) => onChange({ ...draft, endsAt: event.currentTarget.value })} /></label>
    <label className="text-sm font-medium">Source type<select className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.sourceType} onChange={(event) => { const sourceType = event.currentTarget.value as "playlist" | "media"; onChange({ ...draft, sourceType, playlistId: sourceType === "playlist" ? (playlists[0]?.id || draft.playlistId) : undefined, assetId: sourceType === "media" ? (mediaOptions[0]?.assetId || draft.assetId) : undefined }); }} disabled={saving}><option value="playlist">Playlist</option><option value="media">Single Media</option></select></label>
    {draft.sourceType === "playlist" && <label className="text-sm font-medium">Playlist<select required className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.playlistId ?? ""} onChange={(event) => onChange({ ...draft, playlistId: event.currentTarget.value || undefined })} disabled={saving}><option value="">Choose playlist</option>{playlists.map((playlist) => <option key={playlist.id} value={playlist.id}>{playlist.name}</option>)}</select></label>}
    {draft.sourceType === "media" && <label className="text-sm font-medium">Media<select required className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.assetId ?? ""} onChange={(event) => onChange({ ...draft, assetId: event.currentTarget.value || undefined })} disabled={saving}><option value="">Choose media</option>{mediaOptions.map((media) => <option key={media.assetId} value={media.assetId}>{media.title || "Untitled media"}</option>)}</select></label>}
    {error && <p role="alert" className="text-sm text-status-danger sm:col-span-2">{error}</p>}
    <div className="flex gap-2 sm:col-span-2"><Button type="submit" disabled={saving || (draft.sourceType === "playlist" && !draft.playlistId) || (draft.sourceType === "media" && !draft.assetId)}>{saving ? "Saving…" : "Save block"}</Button><Button type="button" variant="secondary" onClick={onCancel}>Cancel</Button></div>
  </form></CardContent></Card>;
}

export function BlockAuthoringDetailPage({ blockId, querySeam }: { blockId: string; querySeam?: AuthoringQuerySeam }) {
  const queryClient = useQueryClient();
  const query = useQuery({ queryKey: ["authoring", "block", blockId], queryFn: () => getBlock(blockId) });
  const playlist = useQuery({ queryKey: ["authoring", "block", blockId, "playlist", query.data?.playlistId], queryFn: () => getPlaylist(query.data!.playlistId!), enabled: Boolean(query.data?.playlistId) });
  const playlists = useQuery({ queryKey: ["authoring", "playlists"], queryFn: listPlaylists });
  const mediaQuery = useQuery({ queryKey: ["authoring", "media-library"], queryFn: listMedia });
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
  return <QueryBoundary query={query}>{query.data && <PageFrame title={query.data.title} description="Inspect the authored source, schedule window, and compiled playback order.">{editing && draft ? <BlockEditForm draft={draft} playlists={playlists.data?.items ?? []} mediaOptions={mediaQuery.data?.items ?? []} saving={saving} error={saveError} onChange={setDraft} onSave={() => void save()} onCancel={() => setEditing(false)} /> : <BlockInspector block={query.data} playlist={playlist.data} visuals={visualQuery.data} visualState={visualQueryNote(querySeam || inheritedSeam ? visualQuery : undefined)} sourceError={playlist.isError} action={<Button type="button" variant="secondary" size="sm" onClick={() => setEditing(true)}>Edit block</Button>} />}</PageFrame>}</QueryBoundary>;
}

export function ProfileAuthoringListPage() {
  const query = useQuery({ queryKey: ["authoring", "profiles"], queryFn: listProfiles });
  const profiles = query.data?.items ?? [];
  return <QueryBoundary query={query}><PageFrame title="Profiles" description="Presentation bundles connecting a Cable lineup, shell, release, and players.">{profiles.length ? <PreviewMosaic label="Presentation profiles" columns={3}>{profiles.map((profile) => <MediaCard key={profile.id} title={profile.name} description={profile.description || "No profile description"} titleHref={authoringLinks.profile(profile.id).href} preview={<EmptyPreview label={`${profile.name} representative content`} description="Open the profile to resolve representative channels." />} status={profile.releaseId ? "ready" : "draft"} statusLabel={profile.releaseId ? "Release linked" : "No release"} />)}</PreviewMosaic> : <EmptyState title="No profiles" description="Create a profile to connect a Cable presentation with releases and players." />}</PageFrame></QueryBoundary>;
}

export interface ProfileInspectorProps {
  profile: CableProfile;
  channels: CableChannel[];
  visuals?: AuthoringVisualSnapshot;
  visualState?: string | null;
  action?: React.ReactNode;
}

export function ProfileInspector({ profile, channels, visuals, visualState, action }: ProfileInspectorProps) {
  const resolved = mediaMap(visuals);
  const references = extractUsageReferences(visuals?.usage);
  const releaseReference = references.find((reference) => reference.kind === "release" && reference.id === profile.releaseId);
  const nodeReferences = references.filter((reference) => reference.kind === "node" || reference.kind === "assignment");
  return <div className="space-y-6">
    <section aria-labelledby="profile-look-heading" className="space-y-3"><div className="flex items-end justify-between"><div><h2 id="profile-look-heading" className="text-lg font-semibold tracking-tight">Representative lineup</h2><p className="text-sm text-muted-foreground">The deployment channels and artwork this profile carries.</p></div>{action}</div>{channels.length ? <PreviewMosaic label={`${profile.name} representative channels`} columns={4}>{channels.slice(0, 4).map((channel) => { const media = resolved.get(channel.artworkAssetId ?? ""); return <MediaCard key={channel.id} title={`${channel.number} · ${channel.name}`} artist={channel.callsign || undefined} titleHref={authoringLinks.channel(channel.id).href} preview={media ? <AuthoringMediaPreview media={media} label={`${channel.name} representative content`} /> : <EmptyPreview label={`${channel.name} representative content`} description={channel.artworkAssetId ? "Artwork Media has not been resolved." : "No artwork is assigned."} />} status={channel.enabled ? channel.standby ? "standby" : "active" : "disabled"} />; })}</PreviewMosaic> : <EmptyState title="No representative channels" description="This profile's deployment has no authored channels yet." />}</section>
    <div className="grid gap-4 md:grid-cols-2"><Card><CardHeader><CardTitle>Linked release</CardTitle></CardHeader><CardContent>{profile.releaseId ? <div className="space-y-2"><StatusSummary status="ready" label="Release assigned" /><a className="inline-block text-sm font-medium text-primary hover:underline" href={releaseReference?.href || authoringLinks.release(profile.releaseId).href}>{releaseReference?.name || "Open assigned release"}</a></div> : <EmptyState title="No release linked" description="Choose a published release while editing this profile." />}</CardContent></Card><Card><CardHeader><CardTitle>Assigned players</CardTitle></CardHeader><CardContent>{visuals?.usage ? nodeReferences.length ? <><p className="mb-3 text-sm text-muted-foreground">{nodeReferences.length} assigned {nodeReferences.length === 1 ? "player" : "players"}</p><ul className="space-y-2">{nodeReferences.map((reference) => <li key={`${reference.kind}:${reference.id}`}><a className="text-sm font-medium text-primary hover:underline" href={reference.href}>{reference.name}</a></li>)}</ul></> : <p className="text-sm text-muted-foreground">No players are assigned to this profile.</p> : <p className="text-sm text-muted-foreground">{visualState || "Assignment usage is not connected on this route."}</p>}</CardContent></Card></div>
    <TechnicalDetails items={[{ label: "Profile ID", value: profile.id }, { label: "Deployment ID", value: profile.deploymentId }, { label: "Shell build ID", value: profile.shellBuildId }, { label: "Release ID", value: profile.releaseId || "Not assigned" }, { label: "Revision", value: `v${profile.version}` }, { label: "Created by", value: profile.createdBy }, { label: "Created", value: formatTimestamp(profile.createdAt) }, { label: "Updated", value: formatTimestamp(profile.updatedAt) }]} />
  </div>;
}

function ProfileEditForm({ draft, deployments, shellBuilds, releases, saving, error, onChange, onSave, onCancel }: { draft: CableProfileInput; deployments: CableDeployment[]; shellBuilds: CableShellBuild[]; releases: ReleaseEnvelope[]; saving: boolean; error: string | null; onChange: (draft: CableProfileInput) => void; onSave: () => void; onCancel: () => void }) {
  return <Card><CardHeader><CardTitle>Edit profile</CardTitle></CardHeader><CardContent><form className="grid gap-4 sm:grid-cols-2" onSubmit={(event) => { event.preventDefault(); onSave(); }}>
    <label className="text-sm font-medium">Name<input required className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.name} onChange={(event) => onChange({ ...draft, name: event.currentTarget.value })} /></label>
    <label className="text-sm font-medium">Deployment<select required className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.deploymentId} onChange={(event) => onChange({ ...draft, deploymentId: event.currentTarget.value })} disabled={saving}><option value="">Choose deployment</option>{deployments.map((deployment) => <option key={deployment.id} value={deployment.id}>{deployment.name}</option>)}</select></label>
    <label className="text-sm font-medium sm:col-span-2">Description<textarea className="mt-1 min-h-24 w-full rounded-md border border-border bg-background p-3" value={draft.description ?? ""} onChange={(event) => onChange({ ...draft, description: event.currentTarget.value })} /></label>
    <label className="text-sm font-medium">Shell build<select required className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.shellBuildId} onChange={(event) => onChange({ ...draft, shellBuildId: event.currentTarget.value })} disabled={saving}><option value="">Choose shell build</option>{shellBuilds.map((build) => <option key={build.id} value={build.id}>{build.versionMetadata?.version || build.digest.slice(0, 12)}</option>)}</select></label>
    <label className="text-sm font-medium">Release<select className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.releaseId ?? ""} onChange={(event) => onChange({ ...draft, releaseId: event.currentTarget.value || undefined })} disabled={saving}><option value="">No release</option>{releases.map((release) => <option key={release.id} value={release.id}>{release.id} · {release.digest.slice(0, 12)}</option>)}</select></label>
    {error && <p role="alert" className="text-sm text-status-danger sm:col-span-2">{error}</p>}
    <div className="flex gap-2 sm:col-span-2"><Button type="submit" disabled={saving || !draft.name.trim() || !draft.deploymentId || !draft.shellBuildId}>{saving ? "Saving…" : "Save profile"}</Button><Button type="button" variant="secondary" onClick={onCancel}>Cancel</Button></div>
  </form></CardContent></Card>;
}

export function ProfileAuthoringDetailPage({ profileId, querySeam }: { profileId: string; querySeam?: AuthoringQuerySeam }) {
  const queryClient = useQueryClient();
  const query = useQuery({ queryKey: ["authoring", "profile", profileId], queryFn: () => getProfile(profileId) });
  const channels = useQuery({ queryKey: ["authoring", "profile", profileId, "channels", query.data?.deploymentId], queryFn: () => listChannels(query.data!.deploymentId), enabled: query.isSuccess });
  const deployments = useQuery({ queryKey: ["authoring", "deployments"], queryFn: listDeployments });
  const shellBuilds = useQuery({ queryKey: ["authoring", "shell-builds"], queryFn: listShellBuilds });
  const releases = useQuery({ queryKey: ["authoring", "releases"], queryFn: listReleases });
  const assetIds = channels.data?.items.map((channel) => channel.artworkAssetId).filter((id): id is string => Boolean(id)) ?? [];
  const visualQuery = useAuthoringVisuals(query.data ? { kind: "profile", id: profileId, assetIds } : null, querySeam);
  const inheritedSeam = React.useContext(AuthoringQueryContext);
  const [editing, setEditing] = React.useState(false);
  const [draft, setDraft] = React.useState<CableProfileInput | null>(null);
  const [saving, setSaving] = React.useState(false);
  const [saveError, setSaveError] = React.useState<string | null>(null);
  React.useEffect(() => { if (query.data) setDraft({ name: query.data.name, description: query.data.description, deploymentId: query.data.deploymentId, shellBuildId: query.data.shellBuildId, releaseId: query.data.releaseId }); }, [query.data]);
  async function save() {
    if (!query.data || !draft) return;
    setSaving(true); setSaveError(null);
    try {
      const result = readApiResult(await api.PATCH("/cable/profiles/{profileId}", { params: { path: { profileId }, header: { "If-Match": authoringIfMatch(query.data.version) } }, body: draft }));
      await queryClient.invalidateQueries({ queryKey: ["authoring", "profile", profileId] });
      queryClient.setQueryData(["authoring", "profile", profileId], result);
      await queryClient.invalidateQueries({ queryKey: ["authoring", "profiles"] });
      setEditing(false);
    } catch (error) { setSaveError(mutationMessage(error, "The profile could not be saved.")); }
    finally { setSaving(false); }
  }
  return <QueryBoundary query={query}>{query.data && <PageFrame title={query.data.name} description={query.data.description || "A Cable lineup, presentation shell, release, and player assignment boundary."}>{editing && draft ? <ProfileEditForm draft={draft} deployments={deployments.data?.items ?? []} shellBuilds={shellBuilds.data?.items ?? []} releases={releases.data?.items ?? []} saving={saving} error={saveError} onChange={setDraft} onSave={() => void save()} onCancel={() => setEditing(false)} /> : <InlineQueryBoundary query={channels}>{channels.data && <ProfileInspector profile={query.data} channels={channels.data.items} visuals={visualQuery.data} visualState={visualQueryNote(querySeam || inheritedSeam ? visualQuery : undefined)} action={<Button type="button" variant="secondary" size="sm" onClick={() => setEditing(true)}>Edit profile</Button>} />}</InlineQueryBoundary>}</PageFrame>}</QueryBoundary>;
}
