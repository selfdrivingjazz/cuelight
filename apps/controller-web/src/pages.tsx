import * as React from "react";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { keepPreviousData, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import type { Asset, components } from "@cuelight/api-client";
import { Button, Card, CardContent, CardHeader, CardTitle, EmptyState, Facts, PageFrame, PageHeader, Pagination, SectionTabs, Skeleton, StatusBadge, TanStackDataTable } from "@cuelight/controller-ui";
import { api, getControllerHealth, readApiResult } from "./api";
import { TokenForm, useAuth } from "./auth";
import { NodeAssignmentComposer } from "./assignment";
import { MediaDropzone } from "./components/MediaDropzone";
import { MediaThumbnail } from "./media-thumbnail";
import { isScreenOffline } from "./fleet-visual";
import { ScreenWall } from "./screens";
import { ThemeControl } from "./theme";
import { LoadingCard, QueryError, RouterLink, RoutePending, SearchBox, Toolbar, formatWhen, getAsset, getNodeStatus, getOperator, getRelease, hasScope, listAssets, listNodeEvents, listNodes, listReleases, listUploads, queryKeys, shortId, type PageSearch } from "./kit";

type ActivityEvent = components["schemas"]["NodeEvent"] & { nodeName: string; nodeId: string };

async function listActivity(): Promise<ActivityEvent[]> {
  const nodes = readApiResult(await api.GET("/nodes", { params: { query: { limit: 100 } } }));
  const pages = await Promise.all(nodes.items.map(async (node) => ({ node, page: await listNodeEvents(node.id) })));
  return pages.flatMap(({ node, page }) => page.items.map((event) => ({ ...event, nodeName: node.name, nodeId: node.id }))).sort((a, b) => b.occurredAt.localeCompare(a.occurredAt));
}

function ActivityTable({ rows }: { rows: ActivityEvent[] }) {
  return <TanStackDataTable caption="Screen activity" rows={rows} rowKey={(event) => event.id} empty={<EmptyState title="No activity" description="No screen has reported events yet." />} columns={[
    { key: "occurred", header: "When", render: (event) => <time className="whitespace-nowrap text-muted-foreground" dateTime={event.occurredAt}>{formatWhen(event.occurredAt)}</time> },
    { key: "node", header: "Screen", render: (event) => <Link className="font-medium hover:text-primary" to="/screens/$nodeId/events" params={{ nodeId: event.nodeId }}>{event.nodeName}</Link> },
    { key: "type", header: "Event", render: (event) => <code className="font-mono text-xs">{event.type}</code> },
  ]} />;
}

function Stat({ label, value, detail, tone }: { label: string; value: React.ReactNode; detail?: React.ReactNode; tone?: "success" | "warning" | "danger" }) {
  return <div className="min-w-0 px-5 py-4">
    <p className="eyebrow">{label}</p>
    <p className={`mt-1.5 text-2xl font-semibold tabular-nums tracking-tight ${tone === "danger" ? "text-status-danger" : tone === "warning" ? "text-status-warning" : ""}`}>{value}</p>
    {detail && <p className="mt-0.5 truncate text-xs text-muted-foreground">{detail}</p>}
  </div>;
}

export function OverviewPage() {
  const health = useQuery({ queryKey: queryKeys.health, queryFn: getControllerHealth });
  const nodes = useQuery({ queryKey: queryKeys.nodes({ limit: 100 }), queryFn: () => listNodes({ limit: 100 }) });
  const items = nodes.data?.items ?? [];
  const reports = useQueries({ queries: items.map((node) => ({ queryKey: queryKeys.nodeStatus(node.id), queryFn: () => getNodeStatus(node.id), staleTime: 5_000 })) });
  const activity = useQuery({ queryKey: queryKeys.activity, queryFn: listActivity });
  const playing = reports.filter((report) => report.data?.currentContent?.state === "playing" && !isScreenOffline(report.data)).length;
  const attention = reports.filter((report) => report.isError || isScreenOffline(report.data ?? undefined) || report.data?.conditions.some((condition) => condition.type === "SafeMode" ? condition.status : !condition.status)).length;
  return <PageFrame>
    <PageHeader title="Overview" description="What every screen is showing, and what needs a look." />
    <section aria-label="Status" className="grid grid-cols-2 divide-border overflow-hidden rounded-lg border border-border bg-card lg:grid-cols-4 [&>*:nth-child(odd)]:border-r [&>*:nth-child(-n+2)]:border-b lg:[&>*]:!border-b-0 lg:[&>*:not(:last-child)]:border-r">
      <Stat label="Controller" value={health.isPending ? "…" : health.isError ? "Unreachable" : health.data.status === "ok" ? "Healthy" : "Degraded"} tone={health.isError || (health.data && health.data.status !== "ok") ? "danger" : undefined} detail={health.data ? `Version ${health.data.version}` : undefined} />
      <Stat label="Screens" value={nodes.isPending ? "…" : `${items.length}${nodes.data?.page.hasMore ? "+" : ""}`} detail="Paired and enrolled" />
      <Stat label="On air" value={nodes.isPending ? "…" : playing} detail={items.length ? `of ${items.length} reporting playback` : "No screens yet"} />
      <Stat label="Need attention" value={nodes.isPending ? "…" : attention} tone={attention ? "warning" : undefined} detail={attention ? "Offline or failing conditions" : "All clear"} />
    </section>
    <section aria-labelledby="wall-heading" className="space-y-3">
      <div className="flex items-baseline justify-between gap-3"><h2 id="wall-heading" className="eyebrow text-foreground/80">Screen wall</h2><Link className="text-sm text-primary hover:underline" to="/screens">All screens →</Link></div>
      {nodes.isPending ? <Skeleton className="aspect-[32/9] w-full" /> : nodes.isError ? <QueryError error={nodes.error} retry={() => void nodes.refetch()} /> : items.length ? <ScreenWall nodes={items.slice(0, 8)} /> : <EmptyState title="No screens yet" description="Pair a TV browser with a code, or enroll the desktop node app." action={<Button asChild><Link to="/pair">Pair a screen</Link></Button>} />}
    </section>
    <div className="grid items-start gap-5 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
      <section aria-labelledby="activity-heading" className="space-y-3">
        <div className="flex items-baseline justify-between gap-3"><h2 id="activity-heading" className="eyebrow text-foreground/80">Recent activity</h2><Link className="text-sm text-primary hover:underline" to="/activity">Activity log →</Link></div>
        {activity.isPending ? <Skeleton className="h-40 w-full" /> : activity.isError ? <QueryError error={activity.error} retry={() => void activity.refetch()} /> : <ActivityTable rows={activity.data.slice(0, 8)} />}
      </section>
      <Card><CardHeader><CardTitle>Shortcuts</CardTitle></CardHeader><CardContent className="grid gap-2">
        {[{ to: "/pair", label: "Pair a screen", hint: "Browser code or desktop token" }, { to: "/screens/assign", label: "Assign content", hint: "Media, profile, or web page" }, { to: "/library/media", label: "Add media", hint: "Upload images and video" }, { to: "/library/playlists", label: "Build a playlist", hint: "Ordered media for a screen" }].map((item) => <Link key={item.to} to={item.to} className="group flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2.5 hover:border-input hover:bg-accent/60"><span><span className="block text-sm font-medium">{item.label}</span><span className="block text-xs text-muted-foreground">{item.hint}</span></span><span aria-hidden="true" className="text-muted-foreground group-hover:text-primary">→</span></Link>)}
      </CardContent></Card>
    </div>
  </PageFrame>;
}

export function ActivityPage() {
  const query = useQuery({ queryKey: queryKeys.activity, queryFn: listActivity });
  return <PageFrame>
    <PageHeader title="Activity" description="Events reported by every screen, newest first." />
    {query.isPending ? <LoadingCard /> : query.isError ? <QueryError error={query.error} retry={() => void query.refetch()} /> : <ActivityTable rows={query.data} />}
  </PageFrame>;
}

function FilesTabs({ active }: { active: "assets" | "uploads" }) {
  return <SectionTabs label="File sections" linkComponent={RouterLink} tabs={[{ label: "Assets", href: "/library/assets", active: active === "assets" }, { label: "Uploads", href: "/library/uploads", active: active === "uploads" }]} />;
}

function cursorNavigation(navigate: (options: { search: (previous: PageSearch) => PageSearch }) => unknown, page: { previousCursor: string | null; nextCursor: string | null }) {
  return {
    onPrevious: () => void navigate({ search: (previous) => ({ ...previous, cursor: page.previousCursor ?? undefined }) }),
    onNext: () => void navigate({ search: (previous) => ({ ...previous, cursor: page.nextCursor ?? undefined }) }),
  };
}

export function AssetsPage() {
  const search = useSearch({ from: "/library/assets" });
  const navigate = useNavigate({ from: "/library/assets" });
  const query = useQuery({ queryKey: queryKeys.assets(search), queryFn: () => listAssets(search), placeholderData: keepPreviousData });
  return <PageFrame>
    <PageHeader title="Files" description="Raw artifacts stored by the controller. Authored media and releases are built from these." />
    <FilesTabs active="assets" />
    <SearchBox value={search.search ?? ""} onChange={(value) => navigate({ search: (previous) => ({ ...previous, search: value || undefined, cursor: undefined }) })} label="Search assets" />
    {query.isPending ? <LoadingCard /> : query.isError ? <QueryError error={query.error} retry={() => void query.refetch()} /> : <>
      <TanStackDataTable caption="Assets" rows={query.data.items} rowKey={(asset) => asset.id} empty={<EmptyState title="No assets" description="Nothing matches this search." />} columns={[
        { key: "name", header: "Asset", render: (asset: Asset) => <Link className="font-medium hover:text-primary" to="/library/assets/$assetId" params={{ assetId: asset.id }}>{asset.name}</Link> },
        { key: "mediaType", header: "Type", render: (asset: Asset) => <code className="font-mono text-xs">{asset.artifact.mediaType}</code> },
        { key: "size", header: "Size", render: (asset: Asset) => <span className="tabular-nums">{(asset.artifact.size / 1_000_000).toFixed(1)} MB</span> },
        { key: "state", header: "State", render: (asset: Asset) => <StatusBadge tone={asset.artifact.state === "ready" ? "success" : "warning"}>{asset.artifact.state ?? "unknown"}</StatusBadge> },
      ]} />
      <Pagination page={query.data.page} {...cursorNavigation(navigate, query.data.page)} />
    </>}
  </PageFrame>;
}

export function AssetDetailPage({ assetId }: { assetId: string }) {
  const query = useQuery({ queryKey: queryKeys.asset(assetId), queryFn: () => getAsset(assetId) });
  // Authored media wraps an asset; when it exists, its poster and editor are the useful context.
  const authored = useQuery({ queryKey: ["controller", "media", "by-asset", assetId], queryFn: async () => readApiResult(await api.GET("/media", { params: { query: { assetId: [assetId], limit: 1, playable: false } } })).items[0] ?? null });
  if (query.isPending) return <RoutePending />;
  if (query.isError) return <PageFrame><PageHeader title="Asset" /><QueryError error={query.error} retry={() => void query.refetch()} /></PageFrame>;
  const asset = query.data;
  const media = authored.data ?? undefined;
  return <PageFrame>
    <PageHeader eyebrow="File" title={asset.name} meta={<p className="text-sm text-muted-foreground">{asset.artifact.mediaType} · {formatFileSize(asset.artifact.size)}</p>}>
      {media && <Button asChild variant="secondary"><Link to="/library/media/$mediaId" params={{ mediaId: media.id }}>Open in media library</Link></Button>}
    </PageHeader>
    <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,2fr)_minmax(17rem,1fr)]">
      <div className="min-w-0 space-y-6">
        {media && <MediaThumbnail media={media} label={`${asset.name} preview`} />}
        {media && <NodeAssignmentComposer fixedMedia={media} title="Show on a screen" />}
      </div>
      <Card><CardHeader><CardTitle>Artifact</CardTitle></CardHeader><CardContent><Facts items={[
        { label: "State", value: <StatusBadge tone={asset.artifact.state === "ready" ? "success" : "warning"}>{asset.artifact.state ?? "unknown"}</StatusBadge> },
        { label: "Size", value: `${asset.artifact.size.toLocaleString()} bytes` },
        { label: "Asset ID", value: asset.id, mono: true, wide: true },
        { label: "Digest", value: asset.artifact.digest, mono: true, wide: true },
      ]} /></CardContent></Card>
    </div>
  </PageFrame>;
}

function formatFileSize(size: number): string {
  if (size < 1_000_000) return `${(size / 1_000).toLocaleString(undefined, { maximumFractionDigits: 1 })} KB`;
  return `${(size / 1_000_000).toLocaleString(undefined, { maximumFractionDigits: 1 })} MB`;
}

export function UploadsPage() {
  const search = useSearch({ from: "/library/uploads" });
  const navigate = useNavigate({ from: "/library/uploads" });
  const client = useQueryClient();
  const query = useQuery({ queryKey: queryKeys.uploads(search), queryFn: () => listUploads(search), placeholderData: keepPreviousData });
  const operator = useQuery({ queryKey: queryKeys.operator, queryFn: getOperator });
  return <PageFrame>
    <PageHeader title="Files" description="Raw artifacts stored by the controller. Authored media and releases are built from these." />
    <FilesTabs active="uploads" />
    <div className="grid items-start gap-5 lg:grid-cols-[minmax(0,1fr)_minmax(0,2fr)]">
      {operator.isPending ? <LoadingCard /> : operator.isError ? <QueryError error={operator.error} retry={() => void operator.refetch()} /> : hasScope(operator.data, "media_write") ? <Card><CardHeader><CardTitle>Upload a file</CardTitle></CardHeader><CardContent><MediaDropzone onUploaded={() => {
        void client.invalidateQueries({ queryKey: ["controller", "uploads"] });
        void client.invalidateQueries({ queryKey: ["controller", "assets"] });
        void client.invalidateQueries({ queryKey: ["controller", "media"] });
      }} /></CardContent></Card> : <EmptyState title="Upload unavailable" description="Your operator token cannot create uploads." />}
      <div className="space-y-3">{query.isPending ? <LoadingCard /> : query.isError ? <QueryError error={query.error} retry={() => void query.refetch()} /> : <>
        <TanStackDataTable caption="Uploads" rows={query.data.items} rowKey={(upload) => upload.id} empty={<EmptyState title="No uploads" description="Upload an image or video to add it to your library." />} columns={[
          { key: "id", header: "Upload", render: (upload) => <code className="font-mono text-xs">{shortId(upload.id)}</code> },
          { key: "progress", header: "Progress", render: (upload) => <span className="tabular-nums">{Math.round((upload.offset / Math.max(upload.length, 1)) * 100)}% of {(upload.length / 1_000_000).toFixed(1)} MB</span> },
          { key: "state", header: "State", render: (upload) => <StatusBadge tone={upload.state === "complete" ? "success" : upload.state === "failed" ? "danger" : "info"}>{upload.state}</StatusBadge> },
        ]} />
        <Pagination page={query.data.page} {...cursorNavigation(navigate, query.data.page)} />
      </>}</div>
    </div>
  </PageFrame>;
}

export function ReleasesPage() {
  const search = useSearch({ from: "/releases" });
  const navigate = useNavigate({ from: "/releases" });
  const query = useQuery({ queryKey: queryKeys.releases(search), queryFn: () => listReleases(search), placeholderData: keepPreviousData });
  return <PageFrame>
    <PageHeader title="Releases" description="Audit log of signed, immutable packages the controller published for assignments. Assign content from Media, Screens, or Profiles." />
    <SearchBox value={search.search ?? ""} onChange={(value) => navigate({ search: (previous) => ({ ...previous, search: value || undefined, cursor: undefined }) })} label="Search releases" />
    {query.isPending ? <LoadingCard /> : query.isError ? <QueryError error={query.error} retry={() => void query.refetch()} /> : <>
      <TanStackDataTable caption="Releases" rows={query.data.items} rowKey={(release) => release.id} empty={<EmptyState title="No releases" description="Assigning content to a screen publishes a release." />} columns={[
        { key: "id", header: "Release", render: (release) => <Link className="font-mono text-xs font-medium hover:text-primary" to="/releases/$releaseId" params={{ releaseId: release.id }}>{shortId(release.id)}</Link> },
        { key: "kind", header: "Presentation", render: (release) => <StatusBadge tone="neutral">{release.payload.presentation?.kind ?? "media"}</StatusBadge> },
        { key: "digest", header: "Digest", render: (release) => <code className="font-mono text-xs text-muted-foreground">{release.digest.slice(0, 16)}…</code> },
        { key: "artifacts", header: "Artifacts", render: (release) => <span className="tabular-nums">{release.payload.artifacts?.length ?? 0}</span> },
      ]} />
      <Pagination page={query.data.page} {...cursorNavigation(navigate, query.data.page)} />
    </>}
  </PageFrame>;
}

export function ReleaseDetailPage({ releaseId }: { releaseId: string }) {
  const query = useQuery({ queryKey: queryKeys.release(releaseId), queryFn: () => getRelease(releaseId) });
  if (query.isPending) return <RoutePending />;
  if (query.isError) return <PageFrame><PageHeader title="Release" /><QueryError error={query.error} retry={() => void query.refetch()} /></PageFrame>;
  const release = query.data;
  return <PageFrame>
    <PageHeader eyebrow="Release" title={`${release.payload.presentation?.kind ?? "media"} · ${shortId(release.id)}`} meta={<code className="break-all font-mono text-xs text-muted-foreground">{release.id}</code>} />
    <div className="grid items-start gap-5 lg:grid-cols-2">
      <div>
        <Card><CardHeader><CardTitle>Payload</CardTitle></CardHeader><CardContent><Facts items={[
          { label: "Presentation", value: release.payload.presentation?.kind ?? "media" },
          { label: "Minimum protocol", value: release.payload.minimumProtocolVersion },
          { label: "Required features", value: release.payload.requiredFeatures?.length ? release.payload.requiredFeatures.join(", ") : "None", wide: true },
        ]} /></CardContent></Card>
      </div>
      <div>
        <Card><CardHeader><CardTitle>Signature</CardTitle></CardHeader><CardContent><Facts columns={1} items={[
          { label: "Digest", value: release.digest, mono: true },
          { label: "Key ID", value: release.keyId, mono: true },
          { label: "Signature", value: release.signature, mono: true },
        ]} /></CardContent></Card>
      </div>
      <div className="space-y-3 lg:col-span-2"><h2 className="eyebrow text-foreground/80">Artifact closure</h2><TanStackDataTable caption="Release artifacts" rows={release.payload.artifacts ?? []} rowKey={(artifact) => artifact.digest} empty={<EmptyState title="No artifacts" description="This release references no artifacts." />} columns={[
        { key: "digest", header: "Digest", render: (artifact) => <code className="break-all font-mono text-xs">{artifact.digest}</code> },
        { key: "mediaType", header: "Type", render: (artifact) => artifact.mediaType },
        { key: "size", header: "Size", render: (artifact) => <span className="tabular-nums">{artifact.size.toLocaleString()} B</span> },
      ]} /></div>
    </div>
  </PageFrame>;
}

export function SettingsPage() {
  const auth = useAuth();
  const health = useQuery({ queryKey: queryKeys.health, queryFn: getControllerHealth });
  const operator = auth.operator;
  return <PageFrame width="narrow">
    <PageHeader title="Settings" description="Appearance and this browser's operator session." />
    <Card><CardHeader><CardTitle>Appearance</CardTitle></CardHeader><CardContent><ThemeControl label="Color theme" /></CardContent></Card>
    <Card><CardHeader><CardTitle>Operator session</CardTitle></CardHeader><CardContent className="space-y-4">
      {operator && <Facts items={[
        operator.authMethod === "network"
          ? { label: "Access", value: `Trusted network (${operator.peerAddress ?? "unknown address"})` }
          : { label: "Token", value: operator.tokenId ?? "Injected by the dev proxy", mono: Boolean(operator.tokenId) },
        { label: "Operator", value: operator.id, mono: true },
        { label: "Scopes", value: <span className="flex flex-wrap gap-1.5">{operator.scopes.map((scope) => <StatusBadge key={scope} tone="neutral">{scope}</StatusBadge>)}</span>, wide: true },
      ]} />}
      {operator?.authMethod === "network" && <div className="space-y-2 border-t border-border pt-4">
        <p className="text-sm text-muted-foreground">This browser is a full operator because control trusts its network. Enter a token to act under that token's identity and scopes instead.</p>
        <TokenForm submitLabel="Use token" />
      </div>}
      {auth.token && <Button variant="secondary" onClick={() => void auth.signOut()}>Sign out</Button>}
    </CardContent></Card>
    <Card><CardHeader><CardTitle>Controller</CardTitle></CardHeader><CardContent>{health.isPending ? <Skeleton className="h-10 w-48" /> : health.isError ? <QueryError error={health.error} retry={() => void health.refetch()} /> : <Facts items={[{ label: "Status", value: <StatusBadge tone={health.data.status === "ok" ? "success" : "danger"}>{health.data.status}</StatusBadge> }, { label: "Version", value: health.data.version, mono: true }]} />}</CardContent></Card>
  </PageFrame>;
}
