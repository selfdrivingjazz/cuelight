import * as React from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "@cuelight/api-client";
import { Button, Card, CardContent, CardHeader, CardTitle, EmptyState, Facts, PageFrame, PageHeader, StatusBadge, TanStackDataTable, type StatusTone } from "@cuelight/controller-ui";
import { api, ApiRequestError, controllerBaseUrl, getApiToken, readApiResult } from "./api";
import { useAuth } from "./auth";
import { LoadingCard, QueryError, formatWhen, hasScope } from "./kit";

export type NodeSoftware = components["schemas"]["NodeSoftwareStatus"];
type SoftwareRelease = components["schemas"]["SoftwareRelease"];
type SoftwareArtifact = components["schemas"]["SoftwareArtifact"];
type ImportResult = components["schemas"]["SoftwareImportResult"];

export const softwareKeys = {
  overview: ["controller", "software", "overview"] as const,
  releases: ["controller", "software", "releases"] as const,
  nodes: ["controller", "software", "nodes"] as const,
};

async function getSoftwareOverview() { return readApiResult(await api.GET("/software")); }
async function listSoftwareReleases() { return readApiResult(await api.GET("/software/releases")).items; }
async function listNodeSoftware() { return readApiResult(await api.GET("/software/nodes")).items; }

/** Software status of every screen, keyed by node id; shared by screen tiles and detail pages. */
export function useNodeSoftware(): Map<string, NodeSoftware> {
  const query = useQuery({ queryKey: softwareKeys.nodes, queryFn: listNodeSoftware, staleTime: 15_000, refetchInterval: 60_000 });
  return React.useMemo(() => new Map((query.data ?? []).map((status) => [status.nodeId, status])), [query.data]);
}

const statusTone: Record<NodeSoftware["status"], StatusTone> = { current: "success", outdated: "warning", updating: "info", failed: "danger", unknown: "neutral", unsupported: "neutral" };
const statusLabel: Record<NodeSoftware["status"], string> = { current: "Up to date", outdated: "Out of date", updating: "Updating", failed: "Update failed", unknown: "Version unknown", unsupported: "Manual updates" };

/** Badges for a screen: out-of-date / updating / failed, and Standby when another network has its display. */
export function SoftwareBadges({ software, showCurrent = false }: { software?: NodeSoftware; showCurrent?: boolean }) {
  if (!software) return null;
  const notable = software.status === "outdated" || software.status === "updating" || software.status === "failed" || showCurrent;
  return <>
    {notable && <StatusBadge tone={statusTone[software.status]}>{statusLabel[software.status]}</StatusBadge>}
    {software.outputStandby && <StatusBadge tone="info">Standby</StatusBadge>}
  </>;
}

/** Banner shown on every page while a newer Cuelight release than this server is imported. */
export function ServerVersionBanner() {
  const overview = useQuery({ queryKey: softwareKeys.overview, queryFn: getSoftwareOverview, staleTime: 60_000, refetchInterval: 300_000, retry: false });
  const control = overview.data?.control;
  if (!control?.updateAvailable) return null;
  return <div role="status" className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-status-warning/40 bg-status-warning/10 px-4 py-2 text-sm sm:px-6 md:px-8">
    <span><strong>Cuelight {control.latestAvailable}</strong> is available — this server runs {control.version}.</span>
    <Link to="/software" className="font-medium text-primary hover:underline">Software</Link>
  </div>;
}

function formatBytes(size: number): string {
  const units = ["B", "KB", "MB", "GB"];
  let value = size;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit += 1; }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

/** Installers people download by hand; updater feeds, blockmaps, and update zips are for the apps themselves. */
function isInstaller(artifact: SoftwareArtifact): boolean {
  if (!artifact.available) return false;
  if (artifact.component === "android") return true;
  return artifact.component === "desktop" && ["dmg", "nsis", "appimage", "deb"].includes(artifact.format);
}

const platformLabel: Record<string, string> = { darwin: "macOS", windows: "Windows", linux: "Linux", android: "Android" };

function DownloadLink({ artifact }: { artifact: SoftwareArtifact }) {
  return <a href={artifact.downloadUrl} download className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2 text-sm hover:border-primary hover:text-primary">
    <span className="min-w-0"><span className="font-medium">{platformLabel[artifact.os] ?? artifact.os}{artifact.arch !== "any" ? ` · ${artifact.arch}` : ""}</span><span className="block truncate font-mono text-xs text-muted-foreground">{artifact.name}</span></span>
    <span className="shrink-0 font-mono text-xs text-muted-foreground">{formatBytes(artifact.size)}</span>
  </a>;
}

function GetTheApp({ release }: { release?: SoftwareRelease }) {
  const installers = release?.artifacts.filter(isInstaller) ?? [];
  const others = release?.artifacts.filter((artifact) => artifact.available && !isInstaller(artifact) && artifact.component !== "desktop") ?? [];
  return <Card><CardHeader><CardTitle>Get the app</CardTitle></CardHeader><CardContent className="space-y-3">
    {!release ? <p className="text-sm text-muted-foreground">Import a release to offer downloads from this server.</p>
      : installers.length === 0 && others.length === 0 ? <p className="text-sm text-muted-foreground">Cuelight {release.version} has no downloaded files on this server yet.</p>
        : <>
          <p className="text-sm text-muted-foreground">Cuelight {release.version}, served by this server — works without internet access.</p>
          <div className="grid gap-2 sm:grid-cols-2">{installers.map((artifact) => <DownloadLink key={artifact.name} artifact={artifact} />)}</div>
          {others.length > 0 && <details className="text-sm"><summary className="cursor-pointer text-muted-foreground">Server and appliance downloads</summary><div className="mt-2 grid gap-2 sm:grid-cols-2">{others.map((artifact) => <DownloadLink key={artifact.name} artifact={artifact} />)}</div></details>}
        </>}
  </CardContent></Card>;
}

function FeedCard({ feed, canWrite }: { feed: components["schemas"]["SoftwareFeedStatus"]; canWrite: boolean }) {
  const client = useQueryClient();
  const check = useMutation({
    mutationFn: async () => readApiResult(await api.POST("/software/check")),
    onSettled: () => { void client.invalidateQueries({ queryKey: ["controller", "software"] }); },
  });
  return <Card><CardHeader className="flex-row items-center justify-between gap-3"><CardTitle>Update feed</CardTitle>
    {canWrite && feed.url && <Button size="sm" variant="secondary" onClick={() => check.mutate()} disabled={check.isPending}>{check.isPending ? "Checking…" : "Check now"}</Button>}
  </CardHeader><CardContent className="space-y-3">
    <Facts columns={1} items={[
      { label: "Feed", value: feed.url || "—", mono: true },
      { label: "Automatic checks", value: feed.enabled ? `Every ${Math.round(feed.intervalSeconds / 3600) || 1}h` : "Off" },
      { label: "Last checked", value: formatWhen(feed.lastCheckedAt) },
      { label: "Last release seen", value: feed.lastVersion || "—" },
    ]} />
    {feed.lastError && <p role="alert" className="break-words rounded-md border border-status-danger/40 bg-status-danger/5 p-3 text-sm text-status-danger">{feed.lastError}</p>}
    {check.isError && <p role="alert" className="text-sm text-destructive">{check.error.message}</p>}
  </CardContent></Card>;
}

/** Streams the envelope first, then every other chosen file; the server verifies as it writes. */
async function uploadRelease(files: File[]): Promise<ImportResult> {
  const envelope = files.find((file) => file.name === "cuelight-release.json");
  if (!envelope) throw new Error("Choose cuelight-release.json together with the release files.");
  const form = new FormData();
  form.append("envelope", envelope, envelope.name);
  for (const file of files) if (file !== envelope) form.append("artifacts", file, file.name);
  const headers = new Headers();
  const token = getApiToken();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const response = await fetch(`${controllerBaseUrl.replace(/\/$/, "")}/software/releases`, { method: "POST", body: form, headers, credentials: "omit" });
  const body = await response.json().catch(() => undefined) as (ImportResult & { detail?: string }) | undefined;
  if (!response.ok) throw new ApiRequestError(response.status, body?.detail || `Import failed (${response.status}).`);
  return body as ImportResult;
}

function ImportCard() {
  const client = useQueryClient();
  const [files, setFiles] = React.useState<File[]>([]);
  const upload = useMutation({
    mutationFn: () => uploadRelease(files),
    onSuccess: () => { setFiles([]); void client.invalidateQueries({ queryKey: ["controller", "software"] }); },
  });
  const result = upload.data;
  return <Card><CardHeader><CardTitle>Import a release</CardTitle></CardHeader><CardContent className="space-y-3">
    <p className="text-sm text-muted-foreground">For networks without internet access: choose <code className="font-mono text-xs">cuelight-release.json</code> and the release files from a Cuelight release. Only releases signed by Cuelight are accepted. On the server, <code className="font-mono text-xs">cuelight-control updates import DIR</code> does the same.</p>
    <input type="file" multiple aria-label="Release files" className="block w-full text-sm" onChange={(event) => setFiles(Array.from(event.currentTarget.files ?? []))} disabled={upload.isPending} />
    <Button onClick={() => upload.mutate()} disabled={upload.isPending || files.length === 0}>{upload.isPending ? "Importing…" : `Import ${files.length || ""} ${files.length === 1 ? "file" : "files"}`}</Button>
    {upload.isError && <p role="alert" className="text-sm text-destructive">{upload.error.message}</p>}
    {result && <p role="status" className="text-sm">Cuelight {result.version} {result.created ? "imported" : "updated"}: {result.artifacts.filter((artifact) => artifact.state !== "missing").length} of {result.artifacts.length} files on this server.</p>}
  </CardContent></Card>;
}

/** Targets each outdated screen at its newest installable release, one rollout per version. */
async function updateOutdated(statuses: NodeSoftware[]) {
  const byVersion = new Map<string, string[]>();
  for (const status of statuses) {
    if (status.status !== "outdated" || !status.latestAvailable) continue;
    byVersion.set(status.latestAvailable, [...(byVersion.get(status.latestAvailable) ?? []), status.nodeId]);
  }
  const skipped: string[] = [];
  for (const [version, nodeIds] of byVersion) {
    const result = readApiResult(await api.POST("/software/rollouts", { body: { version, nodeIds } }));
    skipped.push(...result.skipped.map((skip) => skip.reason));
  }
  if (skipped.length) throw new Error(`Some screens were skipped: ${skipped.join("; ")}`);
}

/** Update / Cancel for one screen. */
export function SoftwareActions({ software }: { software: NodeSoftware }) {
  const client = useQueryClient();
  const change = useMutation({
    mutationFn: async (version: string | null) => {
      const nodeId = software.nodeId;
      return readApiResult(version
        ? await api.PUT("/software/nodes/{nodeId}/target", { params: { path: { nodeId } }, body: { version } })
        : await api.DELETE("/software/nodes/{nodeId}/target", { params: { path: { nodeId } } }));
    },
    onSettled: () => { void client.invalidateQueries({ queryKey: softwareKeys.nodes }); },
  });
  const pending = software.status === "updating" || software.status === "failed";
  return <span className="inline-flex flex-wrap items-center gap-2">
    {software.status === "outdated" && software.latestAvailable && <Button size="sm" onClick={() => change.mutate(software.latestAvailable ?? null)} disabled={change.isPending}>Update to {software.latestAvailable}</Button>}
    {software.status === "failed" && software.target && <Button size="sm" variant="secondary" onClick={() => change.mutate(software.target ?? null)} disabled={change.isPending}>Retry</Button>}
    {pending && software.target && <Button size="sm" variant="ghost" onClick={() => change.mutate(null)} disabled={change.isPending}>Cancel</Button>}
    {change.isError && <span role="alert" className="text-xs text-destructive">{change.error.message}</span>}
  </span>;
}

/** Software card on a screen's System tab. */
export function ScreenSoftwareCard({ nodeId }: { nodeId: string }) {
  const auth = useAuth();
  const query = useQuery({ queryKey: [...softwareKeys.nodes, nodeId], queryFn: async () => readApiResult(await api.GET("/software/nodes/{nodeId}", { params: { path: { nodeId } } })), refetchInterval: 15_000 });
  return <Card><CardHeader><CardTitle>Software</CardTitle></CardHeader><CardContent className="space-y-3">
    {query.isPending ? <LoadingCard /> : query.isError ? <QueryError error={query.error} retry={() => void query.refetch()} /> : <>
      <div className="flex flex-wrap gap-2"><SoftwareBadges software={query.data} showCurrent /></div>
      <Facts items={[
        { label: "Running", value: query.data.running || "Not reported" },
        { label: "Latest available", value: query.data.latestAvailable || "—" },
        { label: "Target", value: query.data.target ? `${query.data.target} · ${formatWhen(query.data.targetRequestedAt)}` : "None" },
        { label: "Updates", value: query.data.updateMethod || "Not reported" },
      ]} />
      {query.data.detail && <p className="text-sm text-muted-foreground">{query.data.detail}</p>}
      {hasScope(auth.operator, "node_write") && <SoftwareActions software={query.data} />}
    </>}
  </CardContent></Card>;
}

export function SoftwarePage() {
  const auth = useAuth();
  const client = useQueryClient();
  const canWrite = hasScope(auth.operator, "node_write");
  const overview = useQuery({ queryKey: softwareKeys.overview, queryFn: getSoftwareOverview });
  const releases = useQuery({ queryKey: softwareKeys.releases, queryFn: listSoftwareReleases });
  const nodes = useQuery({ queryKey: softwareKeys.nodes, queryFn: listNodeSoftware, refetchInterval: 15_000 });
  const outdated = (nodes.data ?? []).filter((status) => status.status === "outdated");
  const rollout = useMutation({ mutationFn: () => updateOutdated(nodes.data ?? []), onSettled: () => { void client.invalidateQueries({ queryKey: softwareKeys.nodes }); } });
  const control = overview.data?.control;
  return <PageFrame>
    <PageHeader title="Software" description="Cuelight versions on this server and its screens. Every release is signed by Cuelight and checked before it is offered." meta={control && <StatusBadge tone={control.updateAvailable ? "warning" : "success"}>Server {control.version}</StatusBadge>}>
      {canWrite && <Button onClick={() => rollout.mutate()} disabled={rollout.isPending || outdated.length === 0}>{rollout.isPending ? "Updating…" : `Update all outdated${outdated.length ? ` (${outdated.length})` : ""}`}</Button>}
    </PageHeader>
    {rollout.isError && <p role="alert" className="text-sm text-destructive">{rollout.error.message}</p>}
    {overview.isPending ? <LoadingCard /> : overview.isError ? <QueryError error={overview.error} retry={() => void overview.refetch()} /> : <div className="grid items-start gap-5 lg:grid-cols-2">
      <Card><CardHeader><CardTitle>This server</CardTitle></CardHeader><CardContent className="space-y-3">
        <Facts items={[{ label: "Running", value: overview.data.control.version }, { label: "Newest release", value: overview.data.control.latestAvailable || "—" }]} />
        {overview.data.control.updateAvailable && <p className="text-sm text-muted-foreground">Upgrade the server with the cuelight-control package for {overview.data.control.latestAvailable}; screens keep playing while it restarts.</p>}
      </CardContent></Card>
      <GetTheApp release={overview.data.latest} />
      <FeedCard feed={overview.data.feed} canWrite={canWrite} />
      {canWrite && <ImportCard />}
    </div>}
    <section className="space-y-3">
      <h2 className="eyebrow text-foreground/80">Screens</h2>
      {nodes.isPending ? <LoadingCard /> : nodes.isError ? <QueryError error={nodes.error} retry={() => void nodes.refetch()} /> : <TanStackDataTable caption="Screen software" rows={nodes.data} rowKey={(row) => row.nodeId} empty={<EmptyState title="No screens" description="Pair a screen to track its software." />} columns={[
        { key: "name", header: "Screen", render: (row) => <Link className="font-medium hover:text-primary" to="/screens/$nodeId/system" params={{ nodeId: row.nodeId }}>{row.nodeName}</Link> },
        { key: "running", header: "Running", render: (row) => <span className="font-mono text-xs">{row.running || "—"}</span> },
        { key: "latest", header: "Latest", render: (row) => <span className="font-mono text-xs">{row.latestAvailable || "—"}</span> },
        { key: "target", header: "Target", render: (row) => <span className="font-mono text-xs">{row.target || "—"}</span> },
        { key: "status", header: "Status", render: (row) => <span className="inline-flex flex-wrap gap-1.5" title={row.detail}><SoftwareBadges software={row} showCurrent /></span> },
        { key: "actions", header: "", render: (row) => canWrite ? <SoftwareActions software={row} /> : null },
      ]} />}
    </section>
    <section className="space-y-3">
      <h2 className="eyebrow text-foreground/80">Releases</h2>
      {releases.isPending ? <LoadingCard /> : releases.isError ? <QueryError error={releases.error} retry={() => void releases.refetch()} /> : <TanStackDataTable caption="Imported releases" rows={releases.data} rowKey={(row) => row.version} empty={<EmptyState title="No releases yet" description="Releases arrive from the update feed, an upload, or cuelight-control updates import." />} columns={[
        { key: "version", header: "Version", render: (row) => <span className="font-mono font-medium">{row.version}</span> },
        { key: "channel", header: "Channel", render: (row) => <StatusBadge tone={row.channel === "stable" ? "success" : "info"}>{row.channel}</StatusBadge> },
        { key: "published", header: "Published", render: (row) => formatWhen(row.publishedAt) },
        { key: "imported", header: "Imported", render: (row) => `${formatWhen(row.importedAt)} · ${row.source}` },
        { key: "files", header: "Files on server", render: (row) => `${row.artifacts.filter((artifact) => artifact.available).length} / ${row.artifacts.length}` },
      ]} />}
    </section>
  </PageFrame>;
}
