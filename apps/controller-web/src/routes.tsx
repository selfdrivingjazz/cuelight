import * as React from "react";
import { createRootRoute, createRoute, createRouter, Link, Outlet, useNavigate, useRouterState, useSearch } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type Asset, type Node, type components } from "@cable/api-client";
import { Breadcrumbs, Button, Card, CardContent, CardHeader, CardTitle, TanStackDataTable, EmptyState, ErrorState, MobileNav, MobileSidebar, Pagination, Sidebar, Skeleton, StatusBadge, type NavigationLink } from "@cable/controller-ui";
import "@cable/controller-ui/styles.css";
import { api, ApiRequestError, getControllerHealth, readApiResult } from "./api";
import { AccessScreen, useAuth } from "./auth";
import { CableChannelDetail, CableDeploymentsPage, CableDeploymentDetail, CableReleaseAssignmentPage, RevisionDetailPage, ShellBuildsPage, uploadSelectedFile } from "./cable";

export interface PageSearch { cursor?: string; limit?: number; search?: string; sort?: string; order?: "asc" | "desc" }
export type NodeSearch = PageSearch & { sort?: "name" | "desiredGeneration" };
const navigation = [
  { label: "Overview", href: "/", section: "Workspace" },
  { label: "Nodes", href: "/nodes", section: "Fleet" },
  { label: "Assets", href: "/assets", section: "Library" },
  { label: "Uploads", href: "/uploads", section: "Library" },
  { label: "Releases", href: "/releases", section: "Releases" },
  { label: "Deployments", href: "/cable/deployments", section: "Cable" },
  { label: "Shell builds", href: "/cable/shell-builds", section: "Cable" },
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
  if (auth.state === "signedOut" || auth.state === "invalid") return <AccessScreen />;
  if (auth.state === "checking" || !auth.operator) return <RoutePending />;
  return <div className="min-h-screen bg-background text-foreground"><div className="flex min-h-screen"><Sidebar items={navigation} currentPath={pathname} linkComponent={RouterLink} /><div className="min-w-0 flex-1"><header className="flex h-16 items-center justify-between border-b border-border px-4 md:px-8"><MobileNav><MobileSidebar items={navigation} currentPath={pathname} linkComponent={RouterLink} /></MobileNav><span className="text-sm text-muted-foreground">{auth.operator.id}</span><Button variant="secondary" size="sm" onClick={auth.signOut}>Log out</Button></header><Outlet /></div></div></div>;
}

const RouterLink = Link as unknown as NavigationLink;
function QueryError({ error, retry }: { error: Error; retry: () => void }) { const status = error instanceof ApiRequestError ? error.status : undefined; const forbidden = status === 403; const notFound = status === 404; return <ErrorState title={forbidden ? "Access denied" : notFound ? "Not found" : "Unable to load data"} description={notFound ? "This resource may have been removed or you may not have access to it." : error.message} onRetry={retry} />; }
function RoutePending() { return <main className="space-y-4 p-6" aria-busy="true"><Skeleton className="h-7 w-48" /><Skeleton className="h-40 w-full" /></main>; }
function RouteError({ error, reset }: { error: Error; reset: () => void }) { return <main className="p-6"><ErrorState title={error instanceof ApiRequestError && error.status === 403 ? "Access denied" : "Unable to load this page"} description={error.message || "The controller returned an unexpected error."} onRetry={reset} /></main>; }
function PageHeader({ title, description, children }: { title: string; description: string; children?: React.ReactNode }) { return <div className="flex flex-col justify-between gap-4 border-b border-border pb-5 md:flex-row md:items-end"><div><h1 className="text-2xl font-semibold tracking-tight">{title}</h1><p className="mt-1 text-sm text-muted-foreground">{description}</p></div>{children && <div className="flex flex-wrap gap-2">{children}</div>}</div>; }
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
      <Card><CardHeader><CardTitle>Operations</CardTitle></CardHeader><CardContent><p className="text-3xl font-semibold">—</p><p className="text-sm text-muted-foreground">Review node activity for current events</p><Link className="mt-4 inline-block text-sm text-primary hover:underline" to="/activity">Open activity</Link></CardContent></Card>
    </>;
  }
  return <PageFrame breadcrumbs={[{ label: "Overview" }]}><PageHeader title="Overview" description="A focused view of controller health and work requiring attention." /><div className="grid gap-4 md:grid-cols-3">{content}</div></PageFrame>;
}

function NodesPage() { const search = useSearch({ from: "/nodes" }); const navigate = useNavigate({ from: "/nodes" }); const query = useQuery({ queryKey: queryKeys.nodes(search), queryFn: () => listNodes(search) }); const setSearch = (value: string) => navigate({ search: (previous) => ({ ...previous, search: value || undefined, cursor: undefined }) }); if (query.isPending) return <RoutePending />; if (query.isError) return <PageFrame breadcrumbs={[{ label: "Overview", href: "/" }, { label: "Fleet" }]}><QueryError error={query.error} retry={() => void query.refetch()} /></PageFrame>; const data = query.data; return <PageFrame breadcrumbs={[{ label: "Overview", href: "/" }, { label: "Fleet" }, { label: "Nodes" }]}><PageHeader title="Nodes" description="Monitor credentials, generations, and observed renderer state." /><Card><CardHeader className="gap-3"><CardTitle>Registered nodes</CardTitle><SearchBox value={search.search ?? ""} onChange={setSearch} label="Search by name or ID" /></CardHeader><CardContent className="space-y-4"><TanStackDataTable caption="Registered nodes" rows={data.items} rowKey={(node) => node.id} empty={<EmptyState title="No matching nodes" description="The controller returned no nodes matching this search." />} columns={[{ key: "name", header: "Node", render: (node: Node) => <Link className="font-medium text-primary hover:underline" to="/nodes/$nodeId" params={{ nodeId: node.id }}>{node.name}</Link> }, { key: "id", header: "ID", render: (node: Node) => <span className="font-mono text-xs">{node.id}</span> }, { key: "credential", header: "Credential", render: (node: Node) => <StatusBadge tone={node.credentialState === "active" ? "success" : "warning"}>{node.credentialState}</StatusBadge> }, { key: "generation", header: "Generation", render: (node: Node) => <span className="tabular-nums">{node.observedGeneration ?? "—"}/{node.desiredGeneration ?? "—"}</span> }]} /><Pagination page={data.page} onPrevious={() => navigate({ search: (previous) => ({ ...previous, cursor: data.page.previousCursor ?? undefined }) })} onNext={() => navigate({ search: (previous) => ({ ...previous, cursor: data.page.nextCursor ?? undefined }) })} /></CardContent></Card></PageFrame>; }

const nodeTabs = [{ label: "Overview", slug: "" }, { label: "Assignment", slug: "assignment" }, { label: "Playback", slug: "playback" }, { label: "Cache", slug: "cache" }, { label: "Capabilities", slug: "capabilities" }, { label: "Events", slug: "events" }] as const;
type NodeTab = typeof nodeTabs[number]["slug"];
type NodeStatus = components["schemas"]["NodeReport"];
type NodeAssignmentData = components["schemas"]["Assignment"];
type NodeEvent = components["schemas"]["NodeEvent"];
type NodeCapabilities = components["schemas"]["CapabilityReport"];
function NodeDetailPage({ nodeId, tab = "" }: { nodeId: string; tab?: NodeTab }) {
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
  const statusContent = status.isPending ? <LoadingCard /> : status.isError ? <QueryError error={status.error} retry={() => void status.refetch()} /> : tab === "playback" ? <NodePlayback status={status.data} /> : tab === "cache" ? <NodeCache status={status.data} /> : <NodeOverview node={detail} status={status.data} />;
  return <PageFrame breadcrumbs={[{ label: "Fleet", href: "/nodes" }, { label: detail.name }, ...(tab ? [{ label: tabTitle }] : [])]}>
    <PageHeader title={detail.name} description={`Node ID ${detail.id}`}><StatusBadge tone={detail.credentialState === "active" ? "success" : "warning"}>{detail.credentialState}</StatusBadge></PageHeader>
    <nav aria-label="Node sections" className="flex gap-1 overflow-x-auto border-b border-border pb-px">{nodeTabs.map((item) => <Link key={item.slug || "overview"} className={`whitespace-nowrap border-b-2 px-3 py-2 text-sm ${item.slug === tab ? "border-primary font-medium text-primary" : "border-transparent text-muted-foreground hover:text-foreground"}`} to={item.slug ? `/nodes/$nodeId/${item.slug}` : "/nodes/$nodeId"} params={{ nodeId }} aria-current={item.slug === tab ? "page" : undefined}>{item.label}</Link>)}</nav>
    {needsStatus && statusContent}
    {tab === "capabilities" && (capabilities.isPending ? <LoadingCard /> : capabilities.isError ? <QueryError error={capabilities.error} retry={() => void capabilities.refetch()} /> : <NodeCapabilityDetails capabilities={capabilities.data} />)}
    {tab === "assignment" && (assignment.isPending ? <LoadingCard /> : assignment.isError ? <QueryError error={assignment.error} retry={() => void assignment.refetch()} /> : <NodeAssignment assignment={assignment.data} />)}
    {tab === "events" && (events.isPending ? <LoadingCard /> : events.isError ? <QueryError error={events.error} retry={() => void events.refetch()} /> : <NodeEvents events={events.data.items} />)}
  </PageFrame>;
}
function NodeOverview({ node, status }: { node: Node; status: NodeStatus }) {
  return <div className="grid gap-4 md:grid-cols-2">
    <Card><CardHeader><CardTitle>Generation</CardTitle></CardHeader><CardContent><dl className="space-y-3 text-sm">
      <div className="flex justify-between"><dt className="text-muted-foreground">Desired generation</dt><dd>{node.desiredGeneration ?? "—"}</dd></div>
      <div className="flex justify-between"><dt className="text-muted-foreground">Observed generation</dt><dd>{node.observedGeneration ?? "—"}</dd></div>
      <div className="flex justify-between"><dt className="text-muted-foreground">Last report sequence</dt><dd>{status.sequence}</dd></div>
    </dl></CardContent></Card>
    <Card><CardHeader><CardTitle>Renderer state</CardTitle></CardHeader><CardContent><dl className="space-y-3 break-all text-sm">
      <div><dt className="text-muted-foreground">Presentation</dt><dd>{status.presentationKind || "—"}</dd></div>
      <div><dt className="text-muted-foreground">Rendering release</dt><dd>{status.renderingReleaseDigest ?? "—"}</dd></div>
      <div><dt className="text-muted-foreground">Healthy release</dt><dd>{status.healthyReleaseDigest ?? "—"}</dd></div>
    </dl></CardContent></Card>
  </div>;
}
function NodePlayback({ status }: { status: NodeStatus }) {
  return <Card><CardHeader><CardTitle>Playback health</CardTitle></CardHeader><CardContent className="space-y-4"><dl className="grid gap-3 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">Presentation</dt><dd>{status.presentationKind || "—"}</dd></div><div><dt className="text-muted-foreground">Healthy release</dt><dd className="break-all font-mono">{status.healthyReleaseDigest ?? "—"}</dd></div></dl><TanStackDataTable caption="Playback conditions" rows={status.conditions} rowKey={(condition) => condition.type} empty={<EmptyState title="No playback conditions" description="The node has not reported playback conditions." />} columns={[{ key: "condition", header: "Condition", render: (condition) => condition.type }, { key: "status", header: "Status", render: (condition) => <StatusBadge tone={condition.status ? "success" : "danger"}>{condition.status ? "Ready" : "Not ready"}</StatusBadge> }, { key: "reason", header: "Reason", render: (condition) => condition.reason }]} /></CardContent></Card>;
}
function NodeCache({ status }: { status: NodeStatus }) {
  return <Card><CardHeader><CardTitle>Cache activation</CardTitle></CardHeader><CardContent><dl className="grid gap-4 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">Candidate release</dt><dd className="break-all font-mono">{status.candidateReleaseDigest ?? "None"}</dd></div><div><dt className="text-muted-foreground">Last known good</dt><dd className="break-all font-mono">{status.lastKnownGoodReleaseDigest ?? "—"}</dd></div></dl></CardContent></Card>;
}
function NodeCapabilityDetails({ capabilities }: { capabilities: NodeCapabilities }) {
  return <Card><CardHeader><CardTitle>Reported capabilities</CardTitle></CardHeader><CardContent><dl className="grid gap-4 text-sm sm:grid-cols-3"><div><dt className="text-muted-foreground">Platform</dt><dd>{capabilities.platform} / {capabilities.architecture}</dd></div><div><dt className="text-muted-foreground">Protocol</dt><dd>{capabilities.protocolVersion}</dd></div><div><dt className="text-muted-foreground">Renderer</dt><dd>{capabilities.rendererVersion}</dd></div><div><dt className="text-muted-foreground">Profile</dt><dd>{capabilities.profileId}</dd></div><div><dt className="text-muted-foreground">Features</dt><dd>{capabilities.features?.join(", ") || "None"}</dd></div><div><dt className="text-muted-foreground">Media types</dt><dd>{capabilities.acceptedMediaTypes?.join(", ") || "None"}</dd></div></dl></CardContent></Card>;
}
function NodeAssignment({ assignment }: { assignment: NodeAssignmentData }) { return <Card><CardHeader><CardTitle>Desired assignment</CardTitle></CardHeader><CardContent><dl className="grid gap-4 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">Release</dt><dd className="font-mono break-all">{assignment.releaseId}</dd></div><div><dt className="text-muted-foreground">Release digest</dt><dd className="font-mono break-all">{assignment.releaseDigest}</dd></div><div><dt className="text-muted-foreground">Delivery mode</dt><dd>{assignment.deliveryMode}</dd></div><div><dt className="text-muted-foreground">Desired generation</dt><dd>{assignment.desiredGeneration}</dd></div></dl></CardContent></Card>; }
function NodeEvents({ events }: { events: NodeEvent[] }) { return <Card><CardHeader><CardTitle>Recent events</CardTitle></CardHeader><CardContent><TanStackDataTable caption="Node events" rows={events} rowKey={(event) => event.id} empty={<EmptyState title="No events" description="The controller has not recorded events for this node." />} columns={[{ key: "sequence", header: "Sequence", render: (event) => <span className="tabular-nums">{event.sequence}</span> }, { key: "type", header: "Type", render: (event) => event.type }, { key: "occurred", header: "Occurred", render: (event) => <time dateTime={event.occurredAt}>{new Date(event.occurredAt).toLocaleString()}</time> }]} /></CardContent></Card>; }
function PaginatedResource<T extends { id: string }>({ title, description, search, data, onSearch, onPrevious, onNext, columns, empty }: { title: string; description: string; search: PageSearch; data: { items: T[]; page: { previousCursor: string | null; nextCursor: string | null } }; onSearch: (value: string) => void; onPrevious: () => void; onNext: () => void; columns: Array<{ key: string; header: string; render: (row: T) => React.ReactNode }>; empty: React.ReactNode }) { return <PageFrame breadcrumbs={[{ label: "Overview", href: "/" }, { label: title }]}><PageHeader title={title} description={description} /><Card><CardHeader className="gap-3"><CardTitle>{title}</CardTitle><SearchBox value={search.search ?? ""} onChange={onSearch} label={`Search ${title.toLocaleLowerCase()}`} /></CardHeader><CardContent className="space-y-4"><TanStackDataTable caption={title} rows={data.items} rowKey={(item) => item.id} columns={columns} empty={empty} /><Pagination page={data.page} onPrevious={onPrevious} onNext={onNext} /></CardContent></Card></PageFrame>; }
function AssetsPage() { const search = useSearch({ from: "/assets" }); const navigate = useNavigate({ from: "/assets" }); const query = useQuery({ queryKey: queryKeys.assets(search), queryFn: () => listAssets(search) }); if (query.isPending) return <RoutePending />; if (query.isError) return <PageFrame breadcrumbs={[{ label: "Overview", href: "/" }, { label: "Assets" }]}><QueryError error={query.error} retry={() => void query.refetch()} /></PageFrame>; return <PaginatedResource title="Assets" description="Browse immutable media artifacts registered with the controller." search={search} data={query.data} onSearch={(value) => navigate({ search: (previous) => ({ ...previous, search: value || undefined, cursor: undefined }) })} onPrevious={() => navigate({ search: (previous) => ({ ...previous, cursor: query.data.page.previousCursor ?? undefined }) })} onNext={() => navigate({ search: (previous) => ({ ...previous, cursor: query.data.page.nextCursor ?? undefined }) })} empty={<EmptyState title="No assets" description="The controller has not registered any assets matching this search." />} columns={[{ key: "name", header: "Asset", render: (asset: Asset) => <Link className="font-medium text-primary hover:underline" to="/assets/$assetId" params={{ assetId: asset.id }}>{asset.name}</Link> }, { key: "mediaType", header: "Media type", render: (asset: Asset) => asset.artifact.mediaType }, { key: "size", header: "Size", render: (asset: Asset) => `${Math.round(asset.artifact.size / 1_000_000)} MB` }, { key: "state", header: "State", render: (asset: Asset) => <StatusBadge tone={asset.artifact.state === "ready" ? "success" : "warning"}>{asset.artifact.state ?? "unknown"}</StatusBadge> }]} />; }
function AssetDetailPage() { const { assetId } = assetDetailRoute.useParams(); const query = useQuery({ queryKey: queryKeys.asset(assetId), queryFn: () => getAsset(assetId) }); if (query.isPending) return <RoutePending />; if (query.isError) return <PageFrame breadcrumbs={[{ label: "Assets", href: "/assets" }, { label: assetId }]}><QueryError error={query.error} retry={() => void query.refetch()} /></PageFrame>; const asset = query.data; return <PageFrame breadcrumbs={[{ label: "Library", href: "/assets" }, { label: asset.name }]}><PageHeader title={asset.name} description={`Asset ID ${asset.id}`} /><Card><CardHeader><CardTitle>Artifact</CardTitle></CardHeader><CardContent><dl className="grid gap-4 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">Digest</dt><dd className="break-all font-mono">{asset.artifact.digest}</dd></div><div><dt className="text-muted-foreground">Media type</dt><dd>{asset.artifact.mediaType}</dd></div><div><dt className="text-muted-foreground">Size</dt><dd>{asset.artifact.size.toLocaleString()} bytes</dd></div><div><dt className="text-muted-foreground">State</dt><dd><StatusBadge tone={asset.artifact.state === "ready" ? "success" : "warning"}>{asset.artifact.state ?? "unknown"}</StatusBadge></dd></div></dl></CardContent></Card></PageFrame>; }
function UploadsPage() {
  const search = useSearch({ from: "/uploads" });
  const navigate = useNavigate({ from: "/uploads" });
  const client = useQueryClient();
  const [file, setFile] = React.useState<File>();
  const query = useQuery({ queryKey: queryKeys.uploads(search), queryFn: () => listUploads(search) });
  const operator = useQuery({ queryKey: queryKeys.operator, queryFn: getOperator });
  const mutation = useMutation({
    mutationFn: async () => {
      if (!file) throw new Error("Choose a file to upload.");
      return uploadSelectedFile(file);
    },
    onSuccess: () => { setFile(undefined); void client.invalidateQueries({ queryKey: ["controller", "uploads"] }); },
  });
  if (query.isPending) return <RoutePending />;
  const canCreateUpload = operator.isSuccess && (operator.data.capabilities.media_write === true || operator.data.scopes.includes("media_write"));
  return <PageFrame breadcrumbs={[{ label: "Overview", href: "/" }, { label: "Library", href: "/assets" }, { label: "Uploads" }]}>
    <PageHeader title="Uploads" description="Stream a selected file through a resumable upload before it becomes an immutable asset." />
    <div className={`grid gap-4 ${canCreateUpload ? "lg:grid-cols-[minmax(0,1fr)_minmax(20rem,28rem)]" : ""}`}>
      <Card><CardHeader><CardTitle>Upload queue</CardTitle></CardHeader><CardContent>{query.isError ? <QueryError error={query.error} retry={() => void query.refetch()} /> : <><TanStackDataTable caption="Uploads" rows={query.data.items} rowKey={(upload) => upload.id} empty={<EmptyState title="No uploads" description="There are no uploads in the controller." />} columns={[{ key: "id", header: "Upload", render: (upload) => <span className="font-mono text-xs">{upload.id}</span> }, { key: "progress", header: "Progress", render: (upload) => `${upload.offset.toLocaleString()} / ${upload.length.toLocaleString()} bytes` }, { key: "state", header: "State", render: (upload) => <StatusBadge tone={upload.state === "complete" ? "success" : upload.state === "failed" ? "danger" : "info"}>{upload.state}</StatusBadge> }]} /><Pagination page={query.data.page} onPrevious={() => navigate({ search: (previous) => ({ ...previous, cursor: query.data.page.previousCursor ?? undefined }) })} onNext={() => navigate({ search: (previous) => ({ ...previous, cursor: query.data.page.nextCursor ?? undefined }) })} /></>}</CardContent></Card>
      {canCreateUpload ? <Card><CardHeader><CardTitle>Upload a file</CardTitle></CardHeader><CardContent><form className="space-y-4" onSubmit={(event) => { event.preventDefault(); mutation.mutate(); }}><label className="block text-sm font-medium" htmlFor="library-upload">File<input id="library-upload" required type="file" onChange={(event) => setFile(event.currentTarget.files?.[0])} className="mt-1 block w-full text-sm" /></label>{mutation.isError && <QueryError error={mutation.error} retry={() => mutation.reset()} />}{mutation.isSuccess && <p className="text-sm text-status-success">File uploaded and asset created.</p>}<Button type="submit" disabled={mutation.isPending || !file}>{mutation.isPending ? "Uploading…" : "Upload file"}</Button></form></CardContent></Card> : operator.isPending ? <LoadingCard /> : operator.isError ? <QueryError error={operator.error} retry={() => void operator.refetch()} /> : <EmptyState title="Upload unavailable" description="Your operator permissions do not include upload creation." />}
    </div>
  </PageFrame>;
}

function ReleasesPage() { const search = useSearch({ from: "/releases" }); const navigate = useNavigate({ from: "/releases" }); const query = useQuery({ queryKey: queryKeys.releases(search), queryFn: () => listReleases(search) }); if (query.isPending) return <RoutePending />; if (query.isError) return <PageFrame breadcrumbs={[{ label: "Overview", href: "/" }, { label: "Releases" }]}><QueryError error={query.error} retry={() => void query.refetch()} /></PageFrame>; return <PaginatedResource title="Releases" description="Inspect immutable signed release envelopes and their artifact closure." search={search} data={query.data} onSearch={(value) => navigate({ search: (previous) => ({ ...previous, search: value || undefined, cursor: undefined }) })} onPrevious={() => navigate({ search: (previous) => ({ ...previous, cursor: query.data.page.previousCursor ?? undefined }) })} onNext={() => navigate({ search: (previous) => ({ ...previous, cursor: query.data.page.nextCursor ?? undefined }) })} empty={<EmptyState title="No releases" description="The controller has not published any releases matching this search." />} columns={[{ key: "id", header: "Release", render: (release) => <Link className="font-mono text-xs text-primary hover:underline" to="/releases/$releaseId" params={{ releaseId: release.id }}>{release.id}</Link> }, { key: "digest", header: "Digest", render: (release) => <span className="font-mono text-xs">{release.digest}</span> }, { key: "protocol", header: "Protocol", render: (release) => release.payload.minimumProtocolVersion }, { key: "artifacts", header: "Artifacts", render: (release) => release.payload.artifacts.length }]} />; }
function ReleasesDetailPage() { const { releaseId } = releaseDetailRoute.useParams(); const query = useQuery({ queryKey: queryKeys.release(releaseId), queryFn: () => getRelease(releaseId) }); if (query.isPending) return <RoutePending />; if (query.isError) return <PageFrame breadcrumbs={[{ label: "Releases", href: "/releases" }, { label: releaseId }]}><QueryError error={query.error} retry={() => void query.refetch()} /></PageFrame>; const release = query.data; return <PageFrame breadcrumbs={[{ label: "Releases", href: "/releases" }, { label: release.id }]}><PageHeader title={`Release ${release.id}`} description="Signed, immutable release envelope" /><div className="grid gap-4 lg:grid-cols-2"><Card><CardHeader><CardTitle>Signature</CardTitle></CardHeader><CardContent><dl className="space-y-3 text-sm"><div><dt className="text-muted-foreground">Digest</dt><dd className="break-all font-mono">{release.digest}</dd></div><div><dt className="text-muted-foreground">Key ID</dt><dd className="break-all font-mono">{release.keyId}</dd></div><div><dt className="text-muted-foreground">Signature</dt><dd className="break-all font-mono">{release.signature}</dd></div></dl></CardContent></Card><Card><CardHeader><CardTitle>Payload</CardTitle></CardHeader><CardContent><dl className="space-y-3 text-sm"><div><dt className="text-muted-foreground">Schema</dt><dd>{release.payload.schemaVersion}</dd></div><div><dt className="text-muted-foreground">Minimum protocol</dt><dd>{release.payload.minimumProtocolVersion}</dd></div><div><dt className="text-muted-foreground">Required features</dt><dd>{release.payload.requiredFeatures.length ? release.payload.requiredFeatures.join(", ") : "None"}</dd></div></dl></CardContent></Card></div><Card><CardHeader><CardTitle>Artifact closure</CardTitle></CardHeader><CardContent><TanStackDataTable caption="Release artifacts" rows={release.payload.artifacts} rowKey={(artifact) => artifact.digest} empty={<EmptyState title="No artifacts" description="This release has no artifact references." />} columns={[{ key: "digest", header: "Digest", render: (artifact) => <span className="break-all font-mono text-xs">{artifact.digest}</span> }, { key: "mediaType", header: "Media type", render: (artifact) => artifact.mediaType }, { key: "size", header: "Size", render: (artifact) => `${artifact.size.toLocaleString()} bytes` }]} /></CardContent></Card></PageFrame>; }

function ActivityPage() { const query = useQuery({ queryKey: queryKeys.activity, queryFn: async () => { const nodes = readApiResult(await api.GET("/nodes", { params: { query: { limit: 100 } } })); const eventPages = await Promise.all(nodes.items.map(async (node) => ({ node, page: await listNodeEvents(node.id) }))); return eventPages.flatMap(({ node, page }) => page.items.map((event) => ({ ...event, nodeName: node.name, nodeId: node.id }))).sort((a, b) => b.occurredAt.localeCompare(a.occurredAt)); } }); if (query.isPending) return <RoutePending />; return <PageFrame breadcrumbs={[{ label: "Overview", href: "/" }, { label: "Operations" }, { label: "Activity" }]}><PageHeader title="Activity" description="Recent node events collected through the controller event stream." />{query.isError ? <QueryError error={query.error} retry={() => void query.refetch()} /> : <Card><CardHeader><CardTitle>Recent events</CardTitle></CardHeader><CardContent><TanStackDataTable caption="Operations activity" rows={query.data} rowKey={(event) => event.id} empty={<EmptyState title="No activity" description="The controller has not reported any node events." />} columns={[{ key: "node", header: "Node", render: (event) => <Link className="text-primary hover:underline" to="/nodes/$nodeId" params={{ nodeId: event.nodeId }}>{event.nodeName}</Link> }, { key: "type", header: "Type", render: (event) => event.type }, { key: "sequence", header: "Sequence", render: (event) => event.sequence }, { key: "occurred", header: "Occurred", render: (event) => <time dateTime={event.occurredAt}>{new Date(event.occurredAt).toLocaleString()}</time> }]} /></CardContent></Card>}</PageFrame>; }
function BoundaryPage({ title, description }: { title: string; description: string }) { return <PageFrame breadcrumbs={[{ label: "Overview", href: "/" }, { label: title }]}><PageHeader title={title} description={description} /><EmptyState title="Navigation boundary" description="Cable authoring is not part of the controller workspace. Use the Cable application to manage deployments." /></PageFrame>; }

const rootRoute = createRootRoute({ component: Shell, pendingComponent: RoutePending, errorComponent: RouteError });
const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: "/", component: OverviewPage });
function NodeOverviewRoute() { const { nodeId } = nodeDetailRoute.useParams(); return <NodeDetailPage nodeId={nodeId} />; }
function NodeAssignmentRoute() { const { nodeId } = nodeAssignmentRoute.useParams(); return <NodeDetailPage nodeId={nodeId} tab="assignment" />; }
function NodePlaybackRoute() { const { nodeId } = nodePlaybackRoute.useParams(); return <NodeDetailPage nodeId={nodeId} tab="playback" />; }
function NodeCacheRoute() { const { nodeId } = nodeCacheRoute.useParams(); return <NodeDetailPage nodeId={nodeId} tab="cache" />; }
function NodeCapabilitiesRoute() { const { nodeId } = nodeCapabilitiesRoute.useParams(); return <NodeDetailPage nodeId={nodeId} tab="capabilities" />; }
function NodeEventsRoute() { const { nodeId } = nodeEventsRoute.useParams(); return <NodeDetailPage nodeId={nodeId} tab="events" />; }
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
const releasesRoute = createRoute({ getParentRoute: () => rootRoute, path: "/releases", validateSearch: normalizePageSearch, component: ReleasesPage });
const releaseDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/releases/$releaseId", component: ReleasesDetailPage });
const deploymentsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/deployments", component: CableDeploymentsPage });
const deploymentDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/deployments/$deploymentId", component: () => <CableDeploymentDetail /> });
const deploymentChannelsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/deployments/$deploymentId/channels", component: () => <CableDeploymentDetail tab="channels" /> });
const deploymentScheduleRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/deployments/$deploymentId/schedule", component: () => <CableDeploymentDetail tab="schedule" /> });
const deploymentRevisionsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/deployments/$deploymentId/revisions", component: () => <CableDeploymentDetail tab="revisions" /> });
const deploymentPublishRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/deployments/$deploymentId/publish", component: () => <CableDeploymentDetail tab="publish" /> });
const channelDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/channels/$channelId", component: () => <CableChannelDetail /> });
const channelProgramsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/channels/$channelId/programs", component: () => <CableChannelDetail tab="programs" /> });
const shellBuildsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/shell-builds", component: ShellBuildsPage });
const revisionDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/revisions/$revisionId", component: RevisionDetailPage });
const cableReleaseRoute = createRoute({ getParentRoute: () => rootRoute, path: "/cable/releases/$releaseId", component: CableReleaseAssignmentPage });
const activityRoute = createRoute({ getParentRoute: () => rootRoute, path: "/activity", component: ActivityPage });
const routeTree = rootRoute.addChildren([indexRoute, nodesRoute, nodeDetailRoute, nodeAssignmentRoute, nodePlaybackRoute, nodeCacheRoute, nodeCapabilitiesRoute, nodeEventsRoute, assetsRoute, assetDetailRoute, uploadsRoute, releasesRoute, releaseDetailRoute, deploymentsRoute, deploymentDetailRoute, deploymentChannelsRoute, deploymentScheduleRoute, deploymentRevisionsRoute, deploymentPublishRoute, channelDetailRoute, channelProgramsRoute, shellBuildsRoute, revisionDetailRoute, cableReleaseRoute, activityRoute]);
export const router = createRouter({ routeTree, defaultPreload: "intent" });
declare module "@tanstack/react-router" { interface Register { router: typeof router } }
export type AppRouter = typeof router;
