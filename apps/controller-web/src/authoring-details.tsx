import * as React from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, readApiResult } from "./api";
import type { Asset, components } from "@cable/api-client";
import { Button, Card, CardContent, CardHeader, CardTitle, DataTable, EmptyState, ErrorState, Skeleton, StatusBadge } from "@cable/controller-ui";

type CableChannel = components["schemas"]["CableChannel"];
type CablePlaylist = components["schemas"]["CablePlaylist"];
type CableBlock = components["schemas"]["CableBlock"];
type CableProfile = components["schemas"]["CableProfile"];
type CableChannelInput = components["schemas"]["CableChannelInput"];

type LinkIntent = { href: string; label: string };

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
  const minutes = Math.floor(rounded / 60);
  const remainder = rounded % 60;
  return minutes ? `${minutes}m ${remainder}s` : `${remainder}s`;
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
async function listProfiles(): Promise<{ items: CableProfile[] }> {
  return readApiResult(await api.GET("/cable/profiles", { params: { query: { limit: 100 } } }));
}

async function getBlock(blockId: string): Promise<CableBlock> {
  return readApiResult(await api.GET("/cable/blocks/{blockId}", { params: { path: { blockId } } }));
}

function QueryBoundary({ query, children }: { query: { isPending: boolean; isError: boolean; error: unknown; refetch: () => unknown }; children: React.ReactNode }) {
  if (query.isPending) return <main className="space-y-4 p-6" aria-busy="true"><Skeleton className="h-7 w-48" /><Skeleton className="h-40 w-full" /></main>;
  if (query.isError) return <ErrorState title="Unable to load authoring data" description={query.error instanceof Error ? query.error.message : "The controller returned an unexpected error."} onRetry={() => void query.refetch()} />;
  return <>{children}</>;
}

function ListFrame({ title, children }: { title: string; description?: string; children: React.ReactNode }) {
  return <main className="mx-auto max-w-[var(--content-max-width)] space-y-6 p-4 md:p-8"><h1 className="text-2xl font-semibold tracking-tight">{title}</h1>{children}</main>;
}

function DetailFrame({ title, children }: { title: string; description?: string; children: React.ReactNode }) {
  return <main className="mx-auto max-w-[var(--content-max-width)] space-y-6 p-4 md:p-8"><h1 className="text-2xl font-semibold tracking-tight">{title}</h1>{children}</main>;
}

export function AssetAuthoringListPage() {
  const query = useQuery({ queryKey: ["authoring", "assets"], queryFn: listAssets });
  return <QueryBoundary query={query}><ListFrame title="Media assets" description="Immutable media artifacts available to Cable programming."><Card><CardHeader><CardTitle>Asset library</CardTitle></CardHeader><CardContent><DataTable caption="Media assets" rows={query.data?.items ?? []} rowKey={(asset) => asset.id} empty={<EmptyState title="No assets" description="No media assets have been registered yet." />} columns={[{ key: "name", header: "Asset", render: (asset) => <a className="font-medium text-primary hover:underline" href={authoringLinks.asset(asset.id).href}>{asset.name}</a> }, { key: "type", header: "Type", render: (asset) => asset.artifact.mediaType }, { key: "state", header: "State", render: (asset) => <StatusBadge tone={asset.artifact.state === "ready" ? "success" : "warning"}>{asset.artifact.state ?? "unknown"}</StatusBadge> }]} /></CardContent></Card></ListFrame></QueryBoundary>;
}

export function AssetAuthoringDetailPage({ assetId }: { assetId: string }) {
  const query = useQuery({ queryKey: ["authoring", "asset", assetId], queryFn: () => getAsset(assetId) });
  return <QueryBoundary query={query}><AssetDetail asset={query.data!} /></QueryBoundary>;
}
function AssetDetail({ asset }: { asset: Asset }) {
  return <DetailFrame title={asset.name} description={`Asset ${asset.id}`}><div className="grid gap-4 lg:grid-cols-2"><Card><CardHeader><CardTitle>Artifact provenance</CardTitle></CardHeader><CardContent><dl className="grid gap-4 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">Digest</dt><dd className="break-all font-mono">{asset.artifact.digest}</dd></div><div><dt className="text-muted-foreground">Media type</dt><dd>{asset.artifact.mediaType}</dd></div><div><dt className="text-muted-foreground">Size</dt><dd>{asset.artifact.size.toLocaleString()} bytes</dd></div><div><dt className="text-muted-foreground">State</dt><dd>{asset.artifact.state ?? "Unknown"}</dd></div></dl></CardContent></Card><Card><CardHeader><CardTitle>Usage</CardTitle></CardHeader><CardContent><p className="text-sm text-muted-foreground">Asset membership is shown from each playlist or block detail. No client-only media records are inferred.</p><a className="mt-3 inline-block text-sm text-primary hover:underline" href="/cable/playlists">Browse playlists</a></CardContent></Card></div></DetailFrame>;
}

export function PlaylistAuthoringListPage() {
  const query = useQuery({ queryKey: ["authoring", "playlists"], queryFn: listPlaylists });
  return <QueryBoundary query={query}><ListFrame title="Playlists" description="Ordered media memberships used by scheduled Cable blocks."><Card><CardContent className="pt-5"><DataTable caption="Cable playlists" rows={query.data?.items ?? []} rowKey={(playlist) => playlist.id} empty={<EmptyState title="No playlists" description="Create a playlist before scheduling a block." />} columns={[{ key: "name", header: "Playlist", render: (playlist) => <a className="font-medium text-primary hover:underline" href={authoringLinks.playlist(playlist.id).href}>{playlist.name}</a> }, { key: "items", header: "Items", render: (playlist) => playlist.items.length }, { key: "version", header: "Revision", render: (playlist) => `v${playlist.version}` }]} /></CardContent></Card></ListFrame></QueryBoundary>;
}

export function PlaylistAuthoringDetailPage({ playlistId }: { playlistId: string }) {
  const query = useQuery({ queryKey: ["authoring", "playlist", playlistId], queryFn: () => getPlaylist(playlistId) });
  return <QueryBoundary query={query}><PlaylistDetail playlist={query.data!} /></QueryBoundary>;
}
function PlaylistDetail({ playlist }: { playlist: CablePlaylist }) {
  return <DetailFrame title={playlist.name}>
    {playlist.description && <p className="text-sm text-muted-foreground">{playlist.description}</p>}
    <Card><CardContent className="pt-5"><DataTable
      caption="Playlist items"
      rows={playlist.items}
      rowKey={(item) => item.id}
      empty={<EmptyState title="Playlist is empty" description="Add media to use this playlist in a block." />}
      columns={[
        { key: "position", header: "", render: (item) => <span className="text-muted-foreground">{item.position}</span> },
        { key: "asset", header: "Media", render: (item) => <a className="font-medium text-primary hover:underline" href={authoringLinks.asset(item.assetId).href}>{item.title || item.assetId}</a> },
        { key: "duration", header: "Length", render: (item) => formatDuration(item.durationSeconds) },
      ]}
    /></CardContent></Card>
  </DetailFrame>;
}

export function ChannelAuthoringListPage({ deploymentId }: { deploymentId: string }) {
  const query = useQuery({ queryKey: ["authoring", "channels", deploymentId], queryFn: () => listChannels(deploymentId) });
  return <QueryBoundary query={query}><ListFrame title="Channels" description="Channel identities and schedule entry points for this deployment."><Card><CardContent className="pt-5"><DataTable caption="Cable channels" rows={query.data?.items ?? []} rowKey={(channel) => channel.id} empty={<EmptyState title="No channels" description="This deployment has no channels yet." />} columns={[{ key: "name", header: "Channel", render: (channel) => <a className="font-medium text-primary hover:underline" href={authoringLinks.channel(channel.id).href}>{channel.number} · {channel.name}</a> }, { key: "callsign", header: "Callsign", render: (channel) => channel.callsign || "—" }, { key: "state", header: "State", render: (channel) => <StatusBadge tone={channel.enabled ? "success" : "neutral"}>{channel.enabled ? "Enabled" : "Disabled"}</StatusBadge> }]} /></CardContent></Card></ListFrame></QueryBoundary>;
}

export function ChannelAuthoringDetailPage({ channelId }: { channelId: string }) {
  const queryClient = useQueryClient();
  const query = useQuery({ queryKey: ["authoring", "channel", channelId], queryFn: () => getChannel(channelId) });
  const blocks = useQuery({ queryKey: ["authoring", "channel", channelId, "blocks"], queryFn: () => listBlocks(channelId), enabled: query.isSuccess });
  const [editing, setEditing] = React.useState(false);
  const [draft, setDraft] = React.useState<CableChannelInput | null>(null);
  const [saving, setSaving] = React.useState(false);
  const [saveError, setSaveError] = React.useState<string | null>(null);
  React.useEffect(() => { if (query.data) setDraft({ number: query.data.number, name: query.data.name, callsign: query.data.callsign, description: query.data.description, accent: query.data.accent, artworkAssetId: query.data.artworkAssetId, enabled: query.data.enabled, standby: query.data.standby }); }, [query.data]);
  async function save() {
    if (!query.data! || !draft) return;
    setSaving(true); setSaveError(null);
    try {
      const result = readApiResult(await api.PATCH("/cable/channels/{channelId}", { params: { path: { channelId }, header: { "If-Match": `\"v${query.data?.version ?? 0}\"` } }, body: draft }));
      queryClient.setQueryData(["authoring", "channel", channelId], result);
      setEditing(false);
    } catch (error) { setSaveError(error instanceof Error ? error.message : "The channel could not be saved."); }
    finally { setSaving(false); }
  }
  return <QueryBoundary query={query}><DetailFrame title={`${query.data?.number ?? 0} · ${query.data?.name ?? "Unnamed channel"}`} description={`Channel ${query.data?.id ?? ""} · deployment ${query.data?.deploymentId ?? ""}`}><Card><CardHeader className="items-end"><Button variant="secondary" size="sm" onClick={() => setEditing((value) => !value)}>{editing ? "Cancel" : "Edit"}</Button></CardHeader><CardContent>{editing && draft ? <form className="space-y-3" onSubmit={(event) => { event.preventDefault(); void save(); }}><label className="block text-sm">Name<input className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.currentTarget.value })} /></label><label className="block text-sm">Callsign<input className="mt-1 h-9 w-full rounded-md border border-border bg-background px-3" value={draft.callsign ?? ""} onChange={(event) => setDraft({ ...draft, callsign: event.currentTarget.value })} /></label><Button type="submit" disabled={saving}>{saving ? "Saving…" : "Save with current revision"}</Button>{saveError && <p role="alert" className="text-sm text-status-danger">{saveError}</p>}</form> : <dl className="grid gap-4 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">Callsign</dt><dd>{(query.data?.callsign ?? "") || "—"}</dd></div><div><dt className="text-muted-foreground">Status</dt><dd>{query.data?.enabled ?? false ? "Enabled" : "Disabled"}{query.data?.standby ?? false ? " · Standby" : ""}</dd></div><div className="sm:col-span-2"><dt className="text-muted-foreground">Description</dt><dd>{(query.data?.description ?? "") || "No description"}</dd></div></dl>}</CardContent></Card><Card><CardHeader><CardTitle>Schedule</CardTitle></CardHeader><CardContent><QueryBoundary query={blocks}>{blocks.data! && <DataTable caption="Channel blocks" rows={blocks.data!.items} rowKey={(block) => block.id} empty={<EmptyState title="No scheduled blocks" description="This channel has no authored blocks in the current schedule." />} columns={[{ key: "title", header: "Block", render: (block) => <a className="text-primary hover:underline" href={authoringLinks.block(block.id).href}>{block.title}</a> }, { key: "window", header: "Window", render: (block) => `${formatTimestamp(block.startsAt)} – ${formatTimestamp(block.endsAt)}` }, { key: "source", header: "Source", render: (block) => block.playlistId ? <a className="text-primary hover:underline" href={authoringLinks.playlist(block.playlistId).href}>Playlist</a> : block.assetId ? <a className="text-primary hover:underline" href={authoringLinks.asset(block.assetId).href}>Asset</a> : "Unknown" }]} />}</QueryBoundary></CardContent></Card></DetailFrame></QueryBoundary>;
}

export function BlockAuthoringListPage({ channelId }: { channelId: string }) {
  const query = useQuery({ queryKey: ["authoring", "blocks", channelId], queryFn: () => listBlocks(channelId) });
  return <QueryBoundary query={query}><ListFrame title="Blocks" description="Timed schedule windows and their authored source."><Card><CardContent className="pt-5"><DataTable caption="Cable blocks" rows={query.data?.items ?? []} rowKey={(block) => block.id} empty={<EmptyState title="No blocks" description="No schedule blocks have been authored for this channel." />} columns={[{ key: "title", header: "Block", render: (block) => <a className="text-primary hover:underline" href={authoringLinks.block(block.id).href}>{block.title}</a> }, { key: "window", header: "Window", render: (block) => `${formatTimestamp(block.startsAt)} – ${formatTimestamp(block.endsAt)}` }, { key: "source", header: "Source", render: (block) => block.playlistId ? <a className="text-primary hover:underline" href={authoringLinks.playlist(block.playlistId).href}>Playlist</a> : block.assetId ? <a className="text-primary hover:underline" href={authoringLinks.asset(block.assetId).href}>Asset</a> : "Unknown" }]} /></CardContent></Card></ListFrame></QueryBoundary>;
}

export function BlockAuthoringDetailPage({ blockId }: { blockId: string }) {
  const query = useQuery({ queryKey: ["authoring", "block", blockId], queryFn: () => getBlock(blockId) });
  return <QueryBoundary query={query}><BlockDetail block={query.data!} /></QueryBoundary>;
}
function BlockDetail({ block }: { block: CableBlock }) {
  return <DetailFrame title={block.title} description={`Block ${block.id} · channel ${block.channelId} · revision v${block.version}`}><Card><CardContent className="pt-5"><dl className="grid gap-4 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">Starts</dt><dd>{formatTimestamp(block.startsAt)}</dd></div><div><dt className="text-muted-foreground">Ends</dt><dd>{formatTimestamp(block.endsAt)}</dd></div><div><dt className="text-muted-foreground">Channel</dt><dd><a className="text-primary hover:underline" href={authoringLinks.channel(block.channelId).href}>{block.channelId}</a></dd></div><div><dt className="text-muted-foreground">Source</dt><dd>{block.playlistId ? <a className="text-primary hover:underline" href={authoringLinks.playlist(block.playlistId).href}>Playlist {block.playlistId}</a> : block.assetId ? <a className="text-primary hover:underline" href={authoringLinks.asset(block.assetId).href}>Asset {block.assetId}</a> : "Unknown source"}</dd></div></dl></CardContent></Card></DetailFrame>;
}

export function ProfileAuthoringListPage() {
  const query = useQuery({ queryKey: ["authoring", "profiles"], queryFn: listProfiles });
  return <QueryBoundary query={query}><ListFrame title="Profiles" description="Deployment presentation profiles and release usage."><Card><CardContent className="pt-5"><DataTable caption="Cable profiles" rows={query.data?.items ?? []} rowKey={(profile) => profile.id} empty={<EmptyState title="No profiles" description="No Cable presentation profiles have been authored." />} columns={[{ key: "name", header: "Profile", render: (profile) => <a className="font-medium text-primary hover:underline" href={authoringLinks.profile(profile.id).href}>{profile.name}</a> }, { key: "deployment", header: "Deployment", render: (profile) => <a className="text-primary hover:underline" href={authoringLinks.deployment(profile.deploymentId).href}>{profile.deploymentId}</a> }, { key: "release", header: "Release", render: (profile) => profile.releaseId ? <a className="font-mono text-xs text-primary hover:underline" href={authoringLinks.release(profile.releaseId).href}>{profile.releaseId}</a> : "Unreleased" }]} /></CardContent></Card></ListFrame></QueryBoundary>;
}

export function ProfileAuthoringDetailPage({ profileId }: { profileId: string }) {
  const query = useQuery({ queryKey: ["authoring", "profile", profileId], queryFn: () => getProfile(profileId) });
  return <QueryBoundary query={query}><DetailFrame title={query.data?.name ?? "Unnamed channel"} description={`Profile ${query.data?.id ?? ""} · revision v${query.data?.version ?? 0}`}><Card><CardContent className="pt-5"><dl className="grid gap-4 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">Description</dt><dd>{(query.data?.description ?? "") || "No description"}</dd></div><div><dt className="text-muted-foreground">Deployment</dt><dd><a className="text-primary hover:underline" href={authoringLinks.deployment(query.data?.deploymentId ?? "").href}>{query.data?.deploymentId ?? ""}</a></dd></div><div><dt className="text-muted-foreground">Shell build</dt><dd><a className="text-primary hover:underline" href={authoringLinks.shellBuild(query.data?.shellBuildId ?? "").href}>{query.data?.shellBuildId ?? ""}</a></dd></div><div><dt className="text-muted-foreground">Release assignment</dt><dd>{query.data?.releaseId ?? "" ? <a className="text-primary hover:underline" href={authoringLinks.release(query.data?.releaseId ?? "").href}>{query.data?.releaseId ?? ""}</a> : "No release assigned"}</dd></div></dl></CardContent></Card></DetailFrame></QueryBoundary>;
}

