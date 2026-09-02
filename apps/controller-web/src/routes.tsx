import * as React from "react";
import { createRootRoute, createRoute, createRouter, Link, Outlet, useNavigate, useParams, useRouterState, useSearch } from "@tanstack/react-router";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { type Asset, type Node, type components } from "@cable/api-client";
import { Breadcrumbs, Button, Card, CardContent, CardHeader, CardTitle, TanStackDataTable, EmptyState, ErrorState, MobileNav, MobileSidebar, Pagination, PreviewMosaic, Sidebar, Skeleton, StatusBadge, type NavigationLink } from "@cable/controller-ui";
import "@cable/controller-ui/styles.css";
import { api, ApiRequestError, getControllerHealth, readApiResult } from "./api";
import { AccessScreen, useAuth } from "./auth";
import { CableChannelDetail, CableDeploymentsPage, CableDeploymentDetail, CableReleaseAssignmentPage, CableProgramDetailPage, RevisionDetailPage, ShellBuildsPage } from "./cable";
import { ThemeControl } from "./theme";
import { NodeAssignmentComposer } from "./assignment";
import { BlockAuthoringDetailPage, BlockAuthoringListPage, ChannelAuthoringDetailPage, PlaylistAuthoringDetailPage, PlaylistAuthoringListPage, ProfileAuthoringDetailPage, ProfileAuthoringListPage, type AuthoringQuerySeam } from "./authoring-details";
import { MediaDropzone } from "./components/MediaDropzone";
import { MediaDetailPage, MediaLibraryPage, getMedia, normalizeMediaSearch, useAuthenticatedPreview, type Media } from "./media-library";
import { FleetNodeCard, NodePlaybackView, type NodeReport, type PlaybackMetadata } from "./fleet-visual";

export interface PageSearch { cursor?: string; limit?: number; search?: string; sort?: string; order?: "asc" | "desc" }
export type NodeSearch = PageSearch & { sort?: "name" | "desiredGeneration" };
const navigation = [
  { label: "Fleet", href: "/nodes", section: "" },
  { label: "Media", href: "/media", section: "Library" },
  { label: "Playlists", href: "/cable/playlists", section: "Library" },
  { label: "Blocks", href: "/cable/blocks", section: "Library" },
  { label: "Channels", href: "/cable/channels", section: "Library" },
  { label: "Profiles", href: "/cable/profiles", section: "Library" },
  { label: "Releases", href: "/releases", section: "Publish" },
  { label: "Assign", href: "/assign", section: "Publish" },
  { label: "Deployments", href: "/cable/deployments", section: "System" },
  { label: "Shell builds", href: "/cable/shell-builds", section: "System" },
];
const queryKeys = {
  health: ["controller", "health"] as const,
  operator: ["controller", "operator"] as const,
  nodes: (search: PageSearch) => ["controller", "nodes", search] as const,
  node: (nodeId: string) => ["controller", "node", nodeId] as const,
  nodeStatus: (nodeId: string) => ["controller", "node-status", nodeId] as const,
  nodeCapabilities: (nodeId: string) => ["controller", "node-capabilities", nodeId] as const,
  nodeAssignment: (nodeId: string) => ["controller", "node-assignment", nodeId] as const,
  nodeEvents: (nodeId: string, cursor?: string) => ["controller", "node-events", nodeId, cursor] as const,
  assets: (search: PageSearch) => ["controller", "assets", search] as const,
  asset: (assetId: string) => ["controller", "asset", assetId] as const,
  uploads: (search: PageSearch) => ["controller", "uploads", search] as const,
  releases: (search: PageSearch) => ["controller", "releases", search] as const,
  release: (releaseId: string) => ["controller", "release", releaseId] as const,
  activity: ["controller", "activity"] as const,
};
function pageSearch(search: PageSearch) { return { cursor: search.cursor, limit: search.limit ?? 25, search: search.search || undefined, sort: search.sort, order: search.order }; }
async function listNodes(search: NodeSearch) { return readApiResult(await api.GET("/nodes", { params: { query: pageSearch(search) } })); }
async function getNode(nodeId: string) { return readApiResult(await api.GET("/nodes/{nodeId}", { params: { path: { nodeId } } })); }
async function getNodeStatus(nodeId: string) { return readApiResult(await api.GET("/nodes/{nodeId}/status", { params: { path: { nodeId } } })); }
async function getNodeCapabilities(nodeId: string) { return readApiResult(await api.GET("/nodes/{nodeId}/capabilities", { params: { path: { nodeId } } })); }
async function getNodeAssignment(nodeId: string) { return readApiResult(await api.GET("/nodes/{nodeId}/assignment", { params: { path: { nodeId } } })); }
async function listNodeEvents(nodeId: string, cursor?: string) { return readApiResult(await api.GET("/nodes/{nodeId}/events", { params: { path: { nodeId }, query: { cursor, limit: 50 } } })); }
async function listAssets(search: PageSearch) { return readApiResult(await api.GET("/assets", { params: { query: pageSearch(search) } })); }
async function getAsset(assetId: string) { return readApiResult(await api.GET("/assets/{assetId}", { params: { path: { assetId } } })); }
async function listUploads(search: PageSearch) { return readApiResult(await api.GET("/uploads", { params: { query: pageSearch(search) } })); }
async function getOperator() { return readApiResult(await api.GET("/operator/me")); }
async function listReleases(search: PageSearch) { return readApiResult(await api.GET("/releases", { params: { query: pageSearch(search) } })); }
async function getRelease(releaseId: string) { return readApiResult(await api.GET("/releases/{releaseId}", { params: { path: { releaseId } } })); }
function Shell() {
  const auth = useAuth();
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  if (pathname.startsWith("/remote/")) return <Outlet />;
  if (auth.state === "signedOut" || auth.state === "invalid") return <AccessScreen />;
  if (auth.state === "checking" || !auth.operator) return <RoutePending />;
  return <div className="min-h-screen bg-background text-foreground"><div className="flex min-h-screen"><Sidebar items={navigation} currentPath={pathname} linkComponent={RouterLink} /><div className="min-w-0 flex-1"><header className="flex h-16 items-center border-b border-border px-4 md:px-8"><MobileNav><MobileSidebar items={navigation} currentPath={pathname} linkComponent={RouterLink} /></MobileNav><div className="ml-auto flex items-center gap-2"><span className="hidden text-sm text-muted-foreground sm:inline">{auth.operator.id}</span><ThemeControl />{auth.token && <Button variant="secondary" size="sm" onClick={auth.signOut}>Log out</Button>}</div></header><Outlet /></div></div></div>;
}

const RouterLink = Link as unknown as NavigationLink;
const authoringQuerySeam: AuthoringQuerySeam = {
  async query(request) {
    const [mediaResult, usageResult] = await Promise.all([
      request.assetIds.length
        ? api.GET("/media", { params: { query: { assetId: [...request.assetIds], limit: 100, playable: true } } }).then(readApiResult)
        : Promise.resolve(undefined),
      api.GET("/usage/{kind}/{id}", { params: { path: { kind: request.kind, id: request.id }, query: { limit: 100 } } }).then(readApiResult),
    ]);
    return { media: mediaResult, usage: usageResult };
  },
};
function QueryError({ error, retry }: { error: Error; retry: () => void }) { const status = error instanceof ApiRequestError ? error.status : undefined; const forbidden = status === 403; const notFound = status === 404; return <ErrorState title={forbidden ? "Access denied" : notFound ? "Not found" : "Unable to load data"} description={notFound ? "This resource may have been removed or you may not have access to it." : error.message} onRetry={retry} />; }
function RoutePending() { return <main className="space-y-4 p-6" aria-busy="true"><Skeleton className="h-7 w-48" /><Skeleton className="h-40 w-full" /></main>; }
function RouteError({ error, reset }: { error: Error; reset: () => void }) { return <main className="p-6"><ErrorState title={error instanceof ApiRequestError && error.status === 403 ? "Access denied" : "Unable to load this page"} description={error.message || "The controller returned an unexpected error."} onRetry={reset} /></main>; }
function PageHeader({ title, description, children }: { title: string; description?: string; children?: React.ReactNode }) { return <div className="flex flex-col justify-between gap-4 border-b border-border pb-5 md:flex-row md:items-end"><div><h1 className="text-2xl font-semibold tracking-tight">{title}</h1>{description && <p className="mt-1 text-sm text-muted-foreground">{description}</p>}</div>{children && <div className="flex flex-wrap gap-2">{children}</div>}</div>; }
function PageFrame({ children, breadcrumbs }: { children: React.ReactNode; breadcrumbs: Array<{ label: string; href?: string }> }) { return <main className="mx-auto max-w-[var(--content-max-width)] space-y-6 p-4 md:p-8"><Breadcrumbs items={breadcrumbs} linkComponent={RouterLink} />{children}</main>; }
function LoadingCard() { return <Card><CardContent className="space-y-3 pt-5"><Skeleton className="h-5 w-48" /><Skeleton className="h-24 w-full" /></CardContent></Card>; }
function normalizePageSearch(search: Record<string, unknown>): PageSearch { return { cursor: typeof search.cursor === "string" ? search.cursor : undefined, limit: typeof search.limit === "number" ? Math.min(Math.max(search.limit, 1), 100) : 25, search: typeof search.search === "string" ? search.search : "", sort: typeof search.sort === "string" ? search.sort : undefined, order: search.order === "desc" ? "desc" : "asc" }; }
function SearchBox({ value, onChange, label }: { value: string; onChange: (value: string) => void; label: string }) { return <label className="max-w-sm text-sm"><span className="sr-only">{label}</span><input className="mt-2 h-9 w-full rounded-md border border-border bg-background px-3" value={value} onChange={(event) => onChange(event.currentTarget.value)} placeholder={label} /></label>; }

function OverviewPage() {
  const health = useQuery({ queryKey: queryKeys.health, queryFn: getControllerHealth });
  const nodes = useQuery({ queryKey: queryKeys.nodes({ limit: 1 }), queryFn: () => listNodes({ limit: 1 }) });
  let content: React.ReactNode;
  if (health.isPending || nodes.isPending) {
    content = <LoadingCard />;
  } else if (health.isError) {
    content = <QueryError error={health.error} retry={() => void health.refetch()} />;
  } else if (nodes.isError) {
    content = <QueryError error={nodes.error} retry={() => void nodes.refetch()} />;
  } else {
    content = <>
      <Card><CardHeader><CardTitle>Controller</CardTitle></CardHeader><CardContent><p className="text-3xl font-semibold">{health.data.status === "ok" ? "Healthy" : "Degraded"}</p><p className="text-sm text-muted-foreground">Version {health.data.version}</p></CardContent></Card>
      <Card><CardHeader><CardTitle>Fleet</CardTitle></CardHeader><CardContent><p className="text-3xl font-semibold">{nodes.data.items.length > 0 ? "Available" : "Empty"}</p><p className="text-sm text-muted-foreground">Registered node inventory</p><Link className="mt-4 inline-block text-sm text-primary hover:underline" to="/nodes">View nodes</Link></CardContent></Card>
    </>;
  }
  return <PageFrame breadcrumbs={[{ label: "Overview" }]}><PageHeader title="Overview" /><div className="grid gap-4 md:grid-cols-2">{content}</div></PageFrame>;
}
function playbackMetadata(media: Media, previewUrl?: string): PlaybackMetadata {
  return {
    title: media.title,
    subtitle: media.artist || media.description,
    media,
    mediaPreviewUrl: previewUrl,
  };
}
async function resolveObservedMedia(report?: NodeReport) {
  const content = report?.currentContent;
  if (content?.mediaId) return getMedia(content.mediaId);
  if (!content?.assetId) throw new Error("No observed media identity is available.");
  const page = readApiResult(await api.GET("/media", {
    params: { query: { assetId: [content.assetId], limit: 1, playable: true } },
  }));
  const media = page.items[0];
  if (!media) throw new Error("Observed media is not registered in the Media Library.");
  return { media, etag: `"media:${media.id}:${media.version}"` };
}


function ResolvedNodeVisual({ node, report, detail = false }: { node: Node; report?: NodeReport; detail?: boolean }) {
  const mediaIdentity = report?.currentContent?.mediaId || report?.currentContent?.assetId || "";
  const media = useQuery({ queryKey: ["controller", "node-current-media", mediaIdentity], queryFn: () => resolveObservedMedia(report), enabled: Boolean(mediaIdentity) });
  if (media.data) return <ResolvedNodeVisualWithMedia node={node} report={report} media={media.data.media} detail={detail} />;
  const props = { node, report, lastContactAt: report?.reportedAt, nodeHref: `/nodes/${node.id}` };
  return detail ? <NodePlaybackView {...props} /> : <FleetNodeCard {...props} />;
}

function ResolvedNodeVisualWithMedia({ node, report, media, detail }: { node: Node; report?: NodeReport; media: Media; detail: boolean }) {
  const preview = useAuthenticatedPreview(media);
  const metadata = playbackMetadata(media, preview.status === "ready" ? preview.url : undefined);
  const props = { node, report, lastContactAt: report?.reportedAt, observedNow: metadata, expectedNow: metadata, nodeHref: `/nodes/${node.id}` };
  return detail ? <NodePlaybackView {...props} /> : <FleetNodeCard {...props} />;
}

function NodesPage() {
  const search = useSearch({ from: "/nodes" });
  const navigate = useNavigate({ from: "/nodes" });
  const query = useQuery({ queryKey: queryKeys.nodes(search), queryFn: () => listNodes(search) });
  const operator = useQuery({ queryKey: queryKeys.operator, queryFn: getOperator });
  const enrollment = useMutation({ mutationFn: async () => readApiResult(await api.POST("/enrollment-tokens", { params: { header: { "Idempotency-Key": crypto.randomUUID() } } })) });
  const items = query.data?.items ?? [];
  const reports = useQueries({ queries: items.map((node) => ({ queryKey: queryKeys.nodeStatus(node.id), queryFn: () => getNodeStatus(node.id), staleTime: 5_000 })) });
  const setSearch = (value: string) => navigate({ search: (previous) => ({ ...previous, search: value || undefined, cursor: undefined }) });
  if (query.isPending) return <RoutePending />;
  if (query.isError) return <PageFrame breadcrumbs={[{ label: "Overview", href: "/" }, { label: "Fleet" }]}><QueryError error={query.error} retry={() => void query.refetch()} /></PageFrame>;
  const data = query.data;
  const canEnroll = operator.isSuccess && (operator.data.capabilities.node_enroll === true || operator.data.scopes.includes("node_enroll"));
  return <PageFrame breadcrumbs={[{ label: "Fleet" }]}>
    <PageHeader title="Fleet" description="See what every display is showing now.">{canEnroll && <Button onClick={() => enrollment.mutate()} disabled={enrollment.isPending}>{enrollment.isPending ? "Creating token…" : "Register node"}</Button>}</PageHeader>
    {enrollment.isError && <QueryError error={enrollment.error} retry={() => enrollment.reset()} />}
    {enrollment.data && <Card><CardHeader><CardTitle>One-time enrollment token</CardTitle></CardHeader><CardContent className="space-y-3"><p className="text-sm text-muted-foreground">Enter this secret in Cable Node before it expires. It cannot be retrieved again.</p><div className="flex gap-2"><input aria-label="Enrollment token" readOnly value={enrollment.data.secret} className="h-10 min-w-0 flex-1 rounded border border-border bg-background px-3 font-mono text-xs" /><Button variant="secondary" onClick={() => void navigator.clipboard.writeText(enrollment.data.secret)}>Copy</Button></div><time className="text-xs text-muted-foreground" dateTime={enrollment.data.expiresAt}>Expires {new Date(enrollment.data.expiresAt).toLocaleString()}</time></CardContent></Card>}
    <SearchBox value={search.search ?? ""} onChange={setSearch} label="Search displays" />
    {data.items.length ? <PreviewMosaic label="Fleet displays" columns={2}>{data.items.map((node, index) => <ResolvedNodeVisual key={node.id} node={node} report={reports[index]?.data} />)}</PreviewMosaic> : <EmptyState title="No matching displays" description="Try a different name or register a node." />}
    <Pagination page={data.page} onPrevious={() => navigate({ search: (previous) => ({ ...previous, cursor: data.page.previousCursor ?? undefined }) })} onNext={() => navigate({ search: (previous) => ({ ...previous, cursor: data.page.nextCursor ?? undefined }) })} />
  </PageFrame>;
}

const nodeTabs = [{ label: "Overview", slug: "" }, { label: "Assignment", slug: "assignment" }, { label: "Playback", slug: "playback" }, { label: "Cache", slug: "cache" }, { label: "Capabilities", slug: "capabilities" }, { label: "Events", slug: "events" }] as const;
type NodeTab = typeof nodeTabs[number]["slug"];
type NodeStatus = components["schemas"]["NodeReport"];
type NodeAssignmentData = components["schemas"]["Assignment"];
type NodeEvent = components["schemas"]["NodeEvent"];
type NodeCapabilities = components["schemas"]["CapabilityReport"];
function NodeDetailPage({ nodeId, tab = "" }: { nodeId: string; tab?: NodeTab }) {
  const auth = useAuth();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [confirmUnregister, setConfirmUnregister] = React.useState(false);
  const unregister = useMutation({
    mutationFn: async () => {
      const result = await api.DELETE("/nodes/{nodeId}", { params: { path: { nodeId }, header: { "Idempotency-Key": crypto.randomUUID() } } });
      if (result.error) throw new ApiRequestError(result.response.status, "The node could not be unregistered.");
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["controller", "nodes"] });
      void navigate({ to: "/nodes" });
    },
  });
  const node = useQuery({ queryKey: queryKeys.node(nodeId), queryFn: () => getNode(nodeId) });
  const needsStatus = tab === "" || tab === "playback" || tab === "cache";
  const status = useQuery({ queryKey: queryKeys.nodeStatus(nodeId), queryFn: () => getNodeStatus(nodeId), enabled: needsStatus });
  const capabilities = useQuery({ queryKey: queryKeys.nodeCapabilities(nodeId), queryFn: () => getNodeCapabilities(nodeId), enabled: tab === "capabilities" });
  const assignment = useQuery({ queryKey: queryKeys.nodeAssignment(nodeId), queryFn: () => getNodeAssignment(nodeId), enabled: tab === "assignment" });
  const events = useQuery({ queryKey: queryKeys.nodeEvents(nodeId), queryFn: () => listNodeEvents(nodeId), enabled: tab === "events" });
  if (node.isPending) return <RoutePending />;
  if (node.isError) return <PageFrame breadcrumbs={[{ label: "Nodes", href: "/nodes" }, { label: nodeId }]}><QueryError error={node.error} retry={() => void node.refetch()} /></PageFrame>;
  const detail = node.data;
  const tabTitle = nodeTabs.find((item) => item.slug === tab)?.label ?? "Overview";
  const statusContent = status.isPending ? <LoadingCard /> : status.isError ? <QueryError error={status.error} retry={() => void status.refetch()} /> : tab === "cache" ? <NodeCache status={status.data} /> : <ResolvedNodeVisual node={detail} report={status.data} detail />;
  const canUnregister = auth.operator?.scopes.includes("security_admin") === true || auth.operator?.capabilities.security_admin === true;
  return <PageFrame breadcrumbs={[{ label: "Fleet", href: "/nodes" }, { label: detail.name }, ...(tab ? [{ label: tabTitle }] : [])]}>
    <PageHeader title={detail.name}><StatusBadge tone={detail.credentialState === "active" ? "success" : "warning"}>{detail.credentialState}</StatusBadge>{canUnregister && !confirmUnregister && <Button variant="secondary" onClick={() => setConfirmUnregister(true)}>Unregister node</Button>}{confirmUnregister && <><Button variant="secondary" onClick={() => setConfirmUnregister(false)}>Cancel</Button><Button onClick={() => unregister.mutate()} disabled={unregister.isPending}>{unregister.isPending ? "Unregistering…" : "Confirm unregister"}</Button></>}</PageHeader>
    {unregister.isError && <QueryError error={unregister.error} retry={() => unregister.reset()} />}
    <nav aria-label="Node sections" className="flex gap-1 overflow-x-auto border-b border-border pb-px">{nodeTabs.map((item) => <Link key={item.slug || "overview"} className={`whitespace-nowrap border-b-2 px-3 py-2 text-sm ${item.slug === tab ? "border-primary font-medium text-primary" : "border-transparent text-muted-foreground hover:text-foreground"}`} to={item.slug ? `/nodes/$nodeId/${item.slug}` : "/nodes/$nodeId"} params={{ nodeId }} aria-current={item.slug === tab ? "page" : undefined}>{item.label}</Link>)}</nav>
    {needsStatus && statusContent}
    {tab === "capabilities" && (capabilities.isPending ? <LoadingCard /> : capabilities.isError ? <QueryError error={capabilities.error} retry={() => void capabilities.refetch()} /> : <NodeCapabilityDetails capabilities={capabilities.data} />)}
    {tab === "assignment" && (assignment.isPending ? <LoadingCard /> : assignment.isError ? <QueryError error={assignment.error} retry={() => void assignment.refetch()} /> : <div className="grid gap-4 lg:grid-cols-2"><NodeAssignment assignment={assignment.data} /><NodeAssignmentComposer fixedNodeId={detail.id} title="Change assignment" /></div>)}
    {tab === "events" && (events.isPending ? <LoadingCard /> : events.isError ? <QueryError error={events.error} retry={() => void events.refetch()} /> : <NodeEvents events={events.data.items} />)}
  </PageFrame>;
}
function NodeCache({ status }: { status: NodeStatus }) {
  return <Card><CardHeader><CardTitle>Cache activation</CardTitle></CardHeader><CardContent><dl className="grid gap-4 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">Candidate release</dt><dd className="break-all font-mono">{status.candidateReleaseDigest ?? "None"}</dd></div><div><dt className="text-muted-foreground">Last known good</dt><dd className="break-all font-mono">{status.lastKnownGoodReleaseDigest ?? "—"}</dd></div></dl></CardContent></Card>;
}
function NodeCapabilityDetails({ capabilities }: { capabilities: NodeCapabilities }) {
  return <Card><CardHeader><CardTitle>Reported capabilities</CardTitle></CardHeader><CardContent><dl className="grid gap-4 text-sm sm:grid-cols-3"><div><dt className="text-muted-foreground">Platform</dt><dd>{capabilities.platform} / {capabilities.architecture}</dd></div><div><dt className="text-muted-foreground">Protocol</dt><dd>{capabilities.protocolVersion}</dd></div><div><dt className="text-muted-foreground">Renderer</dt><dd>{capabilities.rendererVersion}</dd></div><div><dt className="text-muted-foreground">Profile</dt><dd>{capabilities.profileId}</dd></div><div><dt className="text-muted-foreground">Features</dt><dd>{capabilities.features?.join(", ") || "None"}</dd></div><div><dt className="text-muted-foreground">Media types</dt><dd>{capabilities.acceptedMediaTypes?.join(", ") || "None"}</dd></div></dl></CardContent></Card>;
}
function NodeAssignment({ assignment }: { assignment: NodeAssignmentData }) { return <Card><CardHeader><CardTitle>Desired assignment</CardTitle></CardHeader><CardContent><dl className="grid gap-4 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">Release</dt><dd className="font-mono break-all">{assignment.releaseId}</dd></div><div><dt className="text-muted-foreground">Release digest</dt><dd className="font-mono break-all">{assignment.releaseDigest}</dd></div><div><dt className="text-muted-foreground">Delivery mode</dt><dd>{assignment.deliveryMode}</dd></div><div><dt className="text-muted-foreground">Desired generation</dt><dd>{assignment.desiredGeneration}</dd></div></dl></CardContent></Card>; }
function NodeEvents({ events }: { events: NodeEvent[] }) { return <Card><CardHeader><CardTitle>Recent events</CardTitle></CardHeader><CardContent><TanStackDataTable caption="Node events" rows={events} rowKey={(event) => event.id} empty={<EmptyState title="No events" description="The controller has not recorded events for this node." />} columns={[{ key: "sequence", header: "Sequence", render: (event) => <span className="tabular-nums">{event.sequence}</span> }, { key: "type", header: "Type", render: (event) => event.type }, { key: "occurred", header: "Occurred", render: (event) => <time dateTime={event.occurredAt}>{new Date(event.occurredAt).toLocaleString()}</time> }]} /></CardContent></Card>; }
function PaginatedResource<T extends { id: string }>({ title, description, search, data, onSearch, onPrevious, onNext, columns, empty }: { title: string; description: string; search: PageSearch; data: { items: T[]; page: { previousCursor: string | null; nextCursor: string | null } }; onSearch: (value: string) => void; onPrevious: () => void; onNext: () => void; columns: Array<{ key: string; header: string; render: (row: T) => React.ReactNode }>; empty: React.ReactNode }) {
  return <PageFrame breadcrumbs={[{ label: "Overview", href: "/" }, { label: title }]}>
    <PageHeader title={title} description={description} />
    <Card><CardContent className="space-y-4 pt-5">
      <SearchBox value={search.search ?? ""} onChange={onSearch} label={`Search ${title.toLocaleLowerCase()}`} />
      <TanStackDataTable caption={title} rows={data.items} rowKey={(item) => item.id} columns={columns} empty={empty} />
      <Pagination page={data.page} onPrevious={onPrevious} onNext={onNext} />
    </CardContent></Card>
  </PageFrame>;
}
function AssetsPage() { const search = useSearch({ from: "/assets" }); const navigate = useNavigate({ from: "/assets" }); const query = useQuery({ queryKey: queryKeys.assets(search), queryFn: () => listAssets(search) }); if (query.isPending) return <RoutePending />; if (query.isError) return <PageFrame breadcrumbs={[{ label: "Overview", href: "/" }, { label: "Assets" }]}><QueryError error={query.error} retry={() => void query.refetch()} /></PageFrame>; return <PaginatedResource title="Assets" description="Browse images and videos ready to show on your displays." search={search} data={query.data} onSearch={(value) => navigate({ search: (previous) => ({ ...previous, search: value || undefined, cursor: undefined }) })} onPrevious={() => navigate({ search: (previous) => ({ ...previous, cursor: query.data.page.previousCursor ?? undefined }) })} onNext={() => navigate({ search: (previous) => ({ ...previous, cursor: query.data.page.nextCursor ?? undefined }) })} empty={<EmptyState title="No assets" description="The controller has not registered any assets matching this search." />} columns={[{ key: "name", header: "Asset", render: (asset: Asset) => <Link className="font-medium text-primary hover:underline" to="/assets/$assetId" params={{ assetId: asset.id }}>{asset.name}</Link> }, { key: "mediaType", header: "Media type", render: (asset: Asset) => asset.artifact.mediaType }, { key: "size", header: "Size", render: (asset: Asset) => `${Math.round(asset.artifact.size / 1_000_000)} MB` }, { key: "state", header: "State", render: (asset: Asset) => <StatusBadge tone={asset.artifact.state === "ready" ? "success" : "warning"}>{asset.artifact.state ?? "unknown"}</StatusBadge> }]} />; }
function AssetDetailPage() { const { assetId } = assetDetailRoute.useParams(); const query = useQuery({ queryKey: queryKeys.asset(assetId), queryFn: () => getAsset(assetId) }); if (query.isPending) return <RoutePending />; if (query.isError) return <PageFrame breadcrumbs={[{ label: "Assets", href: "/assets" }, { label: assetId }]}><QueryError error={query.error} retry={() => void query.refetch()} /></PageFrame>; const asset = query.data; return <PageFrame breadcrumbs={[{ label: "Library", href: "/assets" }, { label: asset.name }]}><PageHeader title={asset.name} description={`Asset ID ${asset.id}`} /><div className="grid gap-4 lg:grid-cols-2"><Card><CardHeader><CardTitle>Artifact</CardTitle></CardHeader><CardContent><dl className="grid gap-4 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">Digest</dt><dd className="break-all font-mono">{asset.artifact.digest}</dd></div><div><dt className="text-muted-foreground">Media type</dt><dd>{asset.artifact.mediaType}</dd></div><div><dt className="text-muted-foreground">Size</dt><dd>{asset.artifact.size.toLocaleString()} bytes</dd></div><div><dt className="text-muted-foreground">State</dt><dd><StatusBadge tone={asset.artifact.state === "ready" ? "success" : "warning"}>{asset.artifact.state ?? "unknown"}</StatusBadge></dd></div></dl></CardContent></Card><NodeAssignmentComposer fixedAsset={{ id: asset.id, name: asset.name }} title="Assign this media" /></div></PageFrame>; }
function UploadsPage() {
  const search = useSearch({ from: "/uploads" });
  const navigate = useNavigate({ from: "/uploads" });
  const client = useQueryClient();
  const query = useQuery({ queryKey: queryKeys.uploads(search), queryFn: () => listUploads(search) });
  const operator = useQuery({ queryKey: queryKeys.operator, queryFn: getOperator });
  if (query.isPending) return <RoutePending />;
  const canCreateUpload = operator.isSuccess && (operator.data.capabilities.media_write === true || operator.data.scopes.includes("media_write"));
  return <PageFrame breadcrumbs={[{ label: "Overview", href: "/" }, { label: "Library", href: "/assets" }, { label: "Uploads" }]}>
    <PageHeader title="Uploads" description="Add images and videos to your library and check recent uploads." />
    <div className="grid gap-4 lg:grid-cols-3">
      {canCreateUpload ? <Card><CardHeader><CardTitle>Upload a file</CardTitle></CardHeader><CardContent><MediaDropzone onUploaded={() => {
        void client.invalidateQueries({ queryKey: ["controller", "uploads"] });
        void client.invalidateQueries({ queryKey: ["controller", "assets"] });
        void client.invalidateQueries({ queryKey: ["controller", "media"] });
      }} /></CardContent></Card> : operator.isPending ? <LoadingCard /> : operator.isError ? <QueryError error={operator.error} retry={() => void operator.refetch()} /> : <EmptyState title="Upload unavailable" description="Your operator permissions do not include upload creation." />}
      <Card className="lg:col-span-2"><CardHeader><CardTitle>Recent uploads</CardTitle></CardHeader><CardContent>{query.isError ? <QueryError error={query.error} retry={() => void query.refetch()} /> : <><TanStackDataTable caption="Uploads" rows={query.data.items} rowKey={(upload) => upload.id} empty={<EmptyState title="No uploads" description="Upload an image or video to add it to your library." />} columns={[{ key: "id", header: "Upload", render: (upload) => <span className="font-mono text-xs">{upload.id}</span> }, { key: "progress", header: "Progress", render: (upload) => `${upload.offset.toLocaleString()} / ${upload.length.toLocaleString()} bytes` }, { key: "state", header: "State", render: (upload) => <StatusBadge tone={upload.state === "complete" ? "success" : upload.state === "failed" ? "danger" : "info"}>{upload.state}</StatusBadge> }]} /><Pagination page={query.data.page} onPrevious={() => navigate({ search: (previous) => ({ ...previous, cursor: query.data.page.previousCursor ?? undefined }) })} onNext={() => navigate({ search: (previous) => ({ ...previous, cursor: query.data.page.nextCursor ?? undefined }) })} /></>}</CardContent></Card>
    </div>
  </PageFrame>;
}

function ReleasesPage() { const search = useSearch({ from: "/releases" }); const navigate = useNavigate({ from: "/releases" }); const query = useQuery({ queryKey: queryKeys.releases(search), queryFn: () => listReleases(search) }); if (query.isPending) return <RoutePending />; if (query.isError) return <PageFrame breadcrumbs={[{ label: "Overview", href: "/" }, { label: "Releases" }]}><QueryError error={query.error} retry={() => void query.refetch()} /></PageFrame>; return <PaginatedResource title="Releases" description="Review content packages that are ready to assign to displays." search={search} data={query.data} onSearch={(value) => navigate({ search: (previous) => ({ ...previous, search: value || undefined, cursor: undefined }) })} onPrevious={() => navigate({ search: (previous) => ({ ...previous, cursor: query.data.page.previousCursor ?? undefined }) })} onNext={() => navigate({ search: (previous) => ({ ...previous, cursor: query.data.page.nextCursor ?? undefined }) })} empty={<EmptyState title="No releases" description="The controller has not published any releases matching this search." />} columns={[{ key: "id", header: "Release", render: (release) => <Link className="font-mono text-xs text-primary hover:underline" to="/releases/$releaseId" params={{ releaseId: release.id }}>{release.id}</Link> }, { key: "digest", header: "Digest", render: (release) => <span className="font-mono text-xs">{release.digest}</span> }, { key: "protocol", header: "Protocol", render: (release) => release.payload.minimumProtocolVersion }, { key: "artifacts", header: "Artifacts", render: (release) => release.payload.artifacts.length }]} />; }
function ReleasesDetailPage() { const { releaseId } = releaseDetailRoute.useParams(); const query = useQuery({ queryKey: queryKeys.release(releaseId), queryFn: () => getRelease(releaseId) }); if (query.isPending) return <RoutePending />; if (query.isError) return <PageFrame breadcrumbs={[{ label: "Releases", href: "/releases" }, { label: releaseId }]}><QueryError error={query.error} retry={() => void query.refetch()} /></PageFrame>; const release = query.data; return <PageFrame breadcrumbs={[{ label: "Releases", href: "/releases" }, { label: release.id }]}><PageHeader title={`Release ${release.id}`} /><div className="grid gap-4 lg:grid-cols-2"><Card><CardHeader><CardTitle>Signature</CardTitle></CardHeader><CardContent><dl className="space-y-3 text-sm"><div><dt className="text-muted-foreground">Digest</dt><dd className="break-all font-mono">{release.digest}</dd></div><div><dt className="text-muted-foreground">Key ID</dt><dd className="break-all font-mono">{release.keyId}</dd></div><div><dt className="text-muted-foreground">Signature</dt><dd className="break-all font-mono">{release.signature}</dd></div></dl></CardContent></Card><Card><CardHeader><CardTitle>Payload</CardTitle></CardHeader><CardContent><dl className="space-y-3 text-sm"><div><dt className="text-muted-foreground">Presentation</dt><dd>{release.payload.presentation?.kind ?? "media"}</dd></div><div><dt className="text-muted-foreground">Minimum protocol</dt><dd>{release.payload.minimumProtocolVersion}</dd></div><div><dt className="text-muted-foreground">Required features</dt><dd>{release.payload.requiredFeatures.length ? release.payload.requiredFeatures.join(", ") : "None"}</dd></div></dl></CardContent></Card><Card><CardHeader><CardTitle>Artifact closure</CardTitle></CardHeader><CardContent><TanStackDataTable caption="Release artifacts" rows={release.payload.artifacts} rowKey={(artifact) => artifact.digest} empty={<EmptyState title="No artifacts" description="This release has no artifact references." />} columns={[{ key: "digest", header: "Digest", render: (artifact) => <span className="break-all font-mono text-xs">{artifact.digest}</span> }, { key: "mediaType", header: "Media type", render: (artifact) => artifact.mediaType }, { key: "size", header: "Size", render: (artifact) => `${artifact.size.toLocaleString()} bytes` }]} /></CardContent></Card><NodeAssignmentComposer fixedReleaseId={release.id} title="Assign this release" /></div></PageFrame>; }

function AssignPage() { return <PageFrame breadcrumbs={[{ label: "Overview", href: "/" }, { label: "Assign" }]}><PageHeader title="Assign content" description="Choose what a display should show, then select where to show it." /><NodeAssignmentComposer /></PageFrame>; }
function RemoteControlPage() {
  const { sessionId = "" } = useParams({ strict: false });
  const token = React.useMemo(() => new URLSearchParams(window.location.hash.slice(1)).get("token") ?? "", []);
  const sequence = React.useRef(Date.now() * 1_000);
  const queue = React.useRef<Promise<void>>(Promise.resolve());
  const dialTimer = React.useRef<number | undefined>(undefined);
  const [dial, setDial] = React.useState("");
  const [status, setStatus] = React.useState<"ready" | "error">(token ? "ready" : "error");
  const send = React.useCallback((key: string) => {
    if (!token || !sessionId) return;
    sequence.current = Math.max(sequence.current + 1, Date.now() * 1_000);
    const body = JSON.stringify({ sequence: sequence.current, type: "key", key });
    queue.current = queue.current.catch(() => undefined).then(async () => {
      const response = await fetch(`/api/v1/remote/sessions/${encodeURIComponent(sessionId)}/events`, {
        method: "POST",
        headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
        body,
        credentials: "omit",
        cache: "no-store",
      });
      if (!response.ok) throw new Error("Remote event rejected");
      setStatus("ready");
    }).catch(() => setStatus("error"));
  }, [sessionId, token]);
  React.useEffect(() => () => { if (dialTimer.current) window.clearTimeout(dialTimer.current); }, []);
  const pushDigit = (digit: number) => {
    if (dialTimer.current) window.clearTimeout(dialTimer.current);
    const next = `${dial}${digit}`.slice(-3);
    setDial(next);
    dialTimer.current = window.setTimeout(() => {
      send(`ChannelNumber:${next}`);
      setDial("");
    }, 900);
  };
  const button = (label: React.ReactNode, key: string, className = "") => <button className={`remote-button ${className}`} type="button" onClick={() => send(key)}>{label}</button>;
  return <main className="remote-shell">
    <section className="remote-body">
      <header className="remote-top"><div><p>Cable</p><h1>Remote</h1></div><span className={`remote-status ${status}`}>{status === "ready" ? "Connected" : "Connection error"}</span></header>
      <div className="remote-rockers">
        <div className="remote-rocker">{button("CH UP", "ChannelUp")}<span>CH</span>{button("CH DOWN", "ChannelDown")}</div>
        <div className="remote-rocker">{button("VOL UP", "VolumeUp")}<span>VOL</span>{button("VOL DOWN", "VolumeDown")}</div>
      </div>
      <div className="remote-dpad">
        {button("▲", "ArrowUp", "up")}
        {button("◀", "ArrowLeft", "left")}
        {button("OK", "Enter", "ok")}
        {button("▶", "ArrowRight", "right")}
        {button("▼", "ArrowDown", "down")}
      </div>
      <div className="remote-actions">{button("Guide", "Guide")}{button("Info", "Info")}{button("Mute", "Mute")}</div>
      <div className="remote-numpad">
        {[1, 2, 3, 4, 5, 6, 7, 8, 9].map((digit) => <button className="remote-button" key={digit} type="button" onClick={() => pushDigit(digit)}>{digit}</button>)}
        <button className="remote-button zero" type="button" onClick={() => pushDigit(0)}>0</button>
      </div>
      {dial && <div className="remote-dial" aria-live="polite">{dial}</div>}
    </section>
  </main>;
}

function ActivityPage() { const query = useQuery({ queryKey: queryKeys.activity, queryFn: async () => { const nodes = readApiResult(await api.GET("/nodes", { params: { query: { limit: 100 } } })); const eventPages = await Promise.all(nodes.items.map(async (node) => ({ node, page: await listNodeEvents(node.id) }))); return eventPages.flatMap(({ node, page }) => page.items.map((event) => ({ ...event, nodeName: node.name, nodeId: node.id }))).sort((a, b) => b.occurredAt.localeCompare(a.occurredAt)); } }); if (query.isPending) return <RoutePending />; return <PageFrame breadcrumbs={[{ label: "Overview", href: "/" }, { label: "Operations" }, { label: "Activity" }]}><PageHeader title="Activity" />{query.isError ? <QueryError error={query.error} retry={() => void query.refetch()} /> : <Card><CardHeader><CardTitle>Recent events</CardTitle></CardHeader><CardContent><TanStackDataTable caption="Operations activity" rows={query.data} rowKey={(event) => event.id} empty={<EmptyState title="No activity" description="The controller has not reported any node events." />} columns={[{ key: "node", header: "Node", render: (event) => <Link className="text-primary hover:underline" to="/nodes/$nodeId" params={{ nodeId: event.nodeId }}>{event.nodeName}</Link> }, { key: "type", header: "Type", render: (event) => event.type }, { key: "sequence", header: "Sequence", render: (event) => event.sequence }, { key: "occurred", header: "Occurred", render: (event) => <time dateTime={event.occurredAt}>{new Date(event.occurredAt).toLocaleString()}</time> }]} /></CardContent></Card>}</PageFrame>; }
function BoundaryPage({ title, description }: { title: string; description: string }) { return <PageFrame breadcrumbs={[{ label: "Overview", href: "/" }, { label: title }]}><PageHeader title={title} description={description} /><EmptyState title="Navigation boundary" description="Cable authoring is not part of the controller workspace. Use the Cable application to manage deployments." /></PageFrame>; }
function AuthoringEntryPage({ kind }: { kind: "Channels" | "Blocks" }) {
  const channels = kind === "Channels";
  return <PageFrame breadcrumbs={[{ label: "Library", href: "/media" }, { label: kind }]}>
    <PageHeader
      title={kind}
      description={channels ? "Channels are organized within a deployment." : "Blocks are organized within a channel."}
    />
    <Card>
      <CardHeader><CardTitle>{channels ? "Choose a deployment" : "Choose a deployment and channel"}</CardTitle></CardHeader>
      <CardContent className="space-y-4">
        <p className="text-sm text-muted-foreground">{channels ? "Open a deployment to browse and author its channel lineup." : "Open a deployment, select a channel, then manage its scheduled blocks."}</p>
        <Button asChild><Link to="/cable/deployments">Browse deployments</Link></Button>
      </CardContent>
    </Card>
  </PageFrame>;
}

const rootRoute = createRootRoute({ component: Shell, pendingComponent: RoutePending, errorComponent: RouteError });
const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: "/", component: OverviewPage });
function NodeOverviewRoute() { const { nodeId } = nodeDetailRoute.useParams(); return <NodeDetailPage nodeId={nodeId} />; }
function NodeAssignmentRoute() { const { nodeId } = nodeAssignmentRoute.useParams(); return <NodeDetailPage nodeId={nodeId} tab="assignment" />; }
function NodePlaybackRoute() { const { nodeId } = nodePlaybackRoute.useParams(); return <NodeDetailPage nodeId={nodeId} tab="playback" />; }
function NodeCacheRoute() { const { nodeId } = nodeCacheRoute.useParams(); return <NodeDetailPage nodeId={nodeId} tab="cache" />; }
function NodeCapabilitiesRoute() { const { nodeId } = nodeCapabilitiesRoute.useParams(); return <NodeDetailPage nodeId={nodeId} tab="capabilities" />; }
function NodeEventsRoute() { const { nodeId } = nodeEventsRoute.useParams(); return <NodeDetailPage nodeId={nodeId} tab="events" />; }
function PlaylistDetailRoute() { const { playlistId = "" } = useParams({ strict: false }); return <PlaylistAuthoringDetailPage playlistId={playlistId} querySeam={authoringQuerySeam} />; }
function BlockListRoute() { const { channelId = "" } = useParams({ strict: false }); return <BlockAuthoringListPage channelId={channelId} />; }
function BlockDetailRoute() { const { blockId = "" } = useParams({ strict: false }); return <BlockAuthoringDetailPage blockId={blockId} querySeam={authoringQuerySeam} />; }
function ProfileDetailRoute() { const { profileId = "" } = useParams({ strict: false }); return <ProfileAuthoringDetailPage profileId={profileId} querySeam={authoringQuerySeam} />; }
function ChannelDetailRoute() { const { channelId = "" } = useParams({ strict: false }); return <ChannelAuthoringDetailPage channelId={channelId} querySeam={authoringQuerySeam} />; }
const nodesRoute = createRoute({ getParentRoute: () => rootRoute, path: "/nodes", validateSearch: (search: Record<string, unknown>): NodeSearch => ({ ...normalizePageSearch(search), sort: search.sort === "desiredGeneration" ? "desiredGeneration" : "name" }), component: NodesPage });
const nodeDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/nodes/$nodeId", component: NodeOverviewRoute });
const nodeAssignmentRoute = createRoute({ getParentRoute: () => rootRoute, path: "/nodes/$nodeId/assignment", component: NodeAssignmentRoute });
const nodePlaybackRoute = createRoute({ getParentRoute: () => rootRoute, path: "/nodes/$nodeId/playback", component: NodePlaybackRoute });
const nodeCacheRoute = createRoute({ getParentRoute: () => rootRoute, path: "/nodes/$nodeId/cache", component: NodeCacheRoute });
const nodeCapabilitiesRoute = createRoute({ getParentRoute: () => rootRoute, path: "/nodes/$nodeId/capabilities", component: NodeCapabilitiesRoute });
const nodeEventsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/nodes/$nodeId/events", component: NodeEventsRoute });
const assetsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/assets", validateSearch: normalizePageSearch, component: AssetsPage });
const assetDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/assets/$assetId", component: AssetDetailPage });
const uploadsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/uploads", validateSearch: normalizePageSearch, component: UploadsPage });
const channelDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/channels/$channelId", component: ChannelDetailRoute });
const mediaRoute = createRoute({ getParentRoute: () => rootRoute, path: "/media", validateSearch: normalizeMediaSearch, component: MediaLibraryPage });
const mediaDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/media/$mediaId", component: MediaDetailPage });
const releasesRoute = createRoute({ getParentRoute: () => rootRoute, path: "/releases", validateSearch: normalizePageSearch, component: ReleasesPage });
const releaseDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/releases/$releaseId", component: ReleasesDetailPage });
const deploymentsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/deployments", component: CableDeploymentsPage });
const deploymentDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/deployments/$deploymentId", component: () => <CableDeploymentDetail /> });
const deploymentChannelsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/deployments/$deploymentId/channels", component: () => <CableDeploymentDetail tab="channels" /> });
const deploymentScheduleRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/deployments/$deploymentId/schedule", component: () => <CableDeploymentDetail tab="schedule" /> });
const deploymentRevisionsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/deployments/$deploymentId/revisions", component: () => <CableDeploymentDetail tab="revisions" /> });
const deploymentPublishRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/deployments/$deploymentId/publish", component: () => <CableDeploymentDetail tab="publish" /> });
const channelProgramsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/channels/$channelId/programs", component: () => <CableChannelDetail tab="programs" /> });
const programDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/programs/$programId", component: CableProgramDetailPage });
const shellBuildsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/shell-builds", component: ShellBuildsPage });
const playlistsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/playlists", component: PlaylistAuthoringListPage });
const playlistDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/playlists/$playlistId", component: PlaylistDetailRoute });
const channelsEntryRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/channels", component: () => <AuthoringEntryPage kind="Channels" /> });
const channelBlocksRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/channels/$channelId/blocks", component: BlockListRoute });
const blockDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/blocks/$blockId", component: BlockDetailRoute });
const blocksEntryRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/blocks", component: () => <AuthoringEntryPage kind="Blocks" /> });
const profilesRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/profiles", component: ProfileAuthoringListPage });
const profileDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/profiles/$profileId", component: ProfileDetailRoute });
const revisionDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/revisions/$revisionId", component: RevisionDetailPage });
const cableReleaseRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/releases/$releaseId", component: CableReleaseAssignmentPage });
const activityRoute = createRoute({ getParentRoute: () => rootRoute, path: "/activity", component: ActivityPage });
const assignRoute = createRoute({ getParentRoute: () => rootRoute, path: "/assign", component: AssignPage });
const remoteControlRoute = createRoute({ getParentRoute: () => rootRoute, path: "/remote/$sessionId", component: RemoteControlPage });
const routeTree = rootRoute.addChildren([indexRoute, remoteControlRoute, nodesRoute, nodeDetailRoute, nodeAssignmentRoute, nodePlaybackRoute, nodeCacheRoute, nodeCapabilitiesRoute, nodeEventsRoute, assignRoute, mediaRoute, mediaDetailRoute, assetsRoute, assetDetailRoute, uploadsRoute, releasesRoute, releaseDetailRoute, deploymentsRoute, deploymentDetailRoute, deploymentChannelsRoute, deploymentScheduleRoute, deploymentRevisionsRoute, deploymentPublishRoute, channelsEntryRoute, channelDetailRoute, channelProgramsRoute, blocksEntryRoute, channelBlocksRoute, blockDetailRoute, playlistsRoute, playlistDetailRoute, profilesRoute, profileDetailRoute, programDetailRoute, shellBuildsRoute, revisionDetailRoute, cableReleaseRoute, activityRoute]);
export const router = createRouter({ routeTree, defaultPreload: "intent" });
declare module "@tanstack/react-router" { interface Register { router: typeof router } }
export type AppRouter = typeof router;
