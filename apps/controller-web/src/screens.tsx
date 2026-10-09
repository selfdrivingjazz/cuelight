import * as React from "react";
import QRCode from "qrcode";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { keepPreviousData, useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import type { Node, components } from "@cuelight/api-client";
import { Button, Card, CardContent, CardHeader, CardTitle, EmptyState, ErrorState, Facts, PageFrame, PageHeader, Pagination, SectionTabs, Skeleton, StatusBadge, TanStackDataTable } from "@cuelight/controller-ui";
import { api, ApiRequestError, controllerBaseUrl, getApiToken, readApiResult } from "./api";
import { useAuth } from "./auth";
import { DeliveryField, NodeAssignmentComposer, fieldClass, type DeliveryChoice } from "./assignment";
import { MediaTargetField } from "./components/MediaPicker";
import { NodePlaybackView, ScreenTile, nextNodeSelection, type FleetNodeVisual, type NodeCapture, type NodeReport, type PlaybackMetadata } from "./fleet-visual";
import { getMedia, type Media } from "./media-library";
import { useMediaThumbnail } from "./media-thumbnail";
import { LoadingCard, QueryError, RouterLink, RoutePending, SearchBox, Toolbar, formatWhen, getNode, getNodeAssignment, getNodeCapabilities, getNodeStatus, hasScope, listNodeEvents, listNodes, queryKeys, shortId } from "./kit";

type ScreenshotFailureCode = "unsupported" | "failed" | "expired" | "timeout" | "cancelled" | "http";

export class ScreenshotLifecycleError extends Error {
  constructor(public readonly code: ScreenshotFailureCode, message: string, public readonly status?: number) {
    super(message);
    this.name = "ScreenshotLifecycleError";
  }
}

export interface ScreenshotJob {
  id: string;
  status: "queued" | "claimed" | "completed" | "failed" | "unsupported" | "expired";
  requestedAt?: string;
  completionAt?: string;
  retentionUntil?: string;
  resultArtifactDigest?: string;
  resultMediaType?: string;
  errorReason?: string;
}

export interface ScreenshotRequestOptions {
  ttlSeconds?: number;
  timeoutMs?: number;
  pollIntervalMs?: number;
  maxPolls?: number;
  signal?: AbortSignal;
}

const screenshotFailureMessage: Record<Exclude<ScreenshotFailureCode, "http" | "timeout" | "cancelled">, string> = {
  unsupported: "This node does not support screenshots.",
  failed: "The node could not capture a screenshot.",
  expired: "The screenshot job expired before the node completed it.",
};

function screenshotURL(path: string): string {
  return `${controllerBaseUrl.replace(/\/$/, "")}${path}`;
}

function screenshotHeaders(): Headers {
  const headers = new Headers({ Accept: "application/json" });
  const token = getApiToken();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  return headers;
}

async function screenshotJSON<T>(path: string, init: RequestInit, signal: AbortSignal): Promise<T> {
  let response: Response;
  try {
    response = await fetch(screenshotURL(path), { ...init, credentials: "omit", cache: "no-store", signal });
  } catch (error) {
    if (signal.aborted) throw error;
    throw new ScreenshotLifecycleError("http", "The screenshot service is unavailable.");
  }
  if (!response.ok) {
    const message = response.status === 404 ? "The screenshot job was not found." : "The screenshot request was rejected.";
    throw new ScreenshotLifecycleError("http", message, response.status);
  }
  return await response.json() as T;
}

function waitForScreenshot(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    if (signal.aborted) {
      reject(signal.reason ?? new ScreenshotLifecycleError("cancelled", "Screenshot refresh cancelled."));
      return;
    }
    const timer = globalThis.setTimeout(resolve, ms);
    signal.addEventListener("abort", () => {
      globalThis.clearTimeout(timer);
      reject(signal.reason ?? new ScreenshotLifecycleError("cancelled", "Screenshot refresh cancelled."));
    }, { once: true });
  });
}

export async function requestNodeScreenshot(nodeId: string, options: ScreenshotRequestOptions = {}): Promise<{ job: ScreenshotJob; capture: NodeCapture }> {
  const timeout = Math.max(1_000, options.timeoutMs ?? 55_000);
  const controller = new AbortController();
  const abort = () => controller.abort(options.signal?.reason ?? new ScreenshotLifecycleError("cancelled", "Screenshot refresh cancelled."));
  options.signal?.addEventListener("abort", abort, { once: true });
  const timer = globalThis.setTimeout(() => controller.abort(new ScreenshotLifecycleError("timeout", "Screenshot refresh timed out.")), timeout);
  try {
    if (options.signal?.aborted) throw new ScreenshotLifecycleError("cancelled", "Screenshot refresh cancelled.");
    const createHeaders = screenshotHeaders();
    createHeaders.set("Content-Type", "application/json");
    createHeaders.set("Idempotency-Key", crypto.randomUUID());
    const created = await screenshotJSON<ScreenshotJob>("/screenshots", {
      method: "POST",
      headers: createHeaders,
      body: JSON.stringify({ nodeId, ttlSeconds: options.ttlSeconds ?? 60 }),
    }, controller.signal);
    if (!created.id) throw new ScreenshotLifecycleError("http", "The screenshot service returned no job ID.");
    let job = created;
    const pollInterval = Math.max(50, options.pollIntervalMs ?? 250);
    const maxPolls = Math.max(1, options.maxPolls ?? Math.ceil(timeout / pollInterval));
    for (let poll = 0; poll < maxPolls && job.status !== "completed"; poll += 1) {
      if (job.status === "unsupported" || job.status === "failed" || job.status === "expired") {
        throw new ScreenshotLifecycleError(job.status, job.errorReason || screenshotFailureMessage[job.status]);
      }
      if (poll > 0) await waitForScreenshot(pollInterval, controller.signal);
      job = await screenshotJSON<ScreenshotJob>(`/screenshots/${encodeURIComponent(job.id)}`, { headers: screenshotHeaders() }, controller.signal);
    }
    if (job.status !== "completed") throw new ScreenshotLifecycleError("timeout", "The screenshot node did not complete before the polling limit.");
    const response = await fetch(screenshotURL(`/screenshots/${encodeURIComponent(job.id)}/content`), { headers: new Headers({ ...Object.fromEntries(screenshotHeaders()), Accept: "image/*" }), credentials: "omit", cache: "no-store", signal: controller.signal });
    if (!response.ok) throw new ScreenshotLifecycleError("http", "The completed screenshot could not be downloaded.", response.status);
    const blob = await response.blob();
    if (!blob.type.startsWith("image/")) throw new ScreenshotLifecycleError("failed", "The screenshot service returned non-image content.");
    const url = URL.createObjectURL(blob);
    return { job, capture: { url, capturedAt: job.completionAt, provenance: job.resultArtifactDigest ? `Node screenshot · ${job.resultArtifactDigest.slice(0, 12)}` : "Node screenshot", alt: `Actual capture from node ${nodeId}` } };
  } catch (error) {
    if (error instanceof ScreenshotLifecycleError) throw error;
    if (controller.signal.aborted) {
      const reason = controller.signal.reason;
      if (reason instanceof ScreenshotLifecycleError) throw reason;
      throw new ScreenshotLifecycleError("cancelled", "Screenshot refresh cancelled.");
    }
    throw error;
  } finally {
    globalThis.clearTimeout(timer);
    options.signal?.removeEventListener("abort", abort);
  }
}
export function disposeNodeCapture(capture?: NodeCapture): void {
  if (capture?.url) URL.revokeObjectURL(capture.url);
}

function NodeScreenshotControl({ nodeId, onCapture }: { nodeId: string; onCapture: (capture: NodeCapture) => void }) {
  const [state, setState] = React.useState<{ status: "idle" | "loading" | "ready" | "error"; capture?: NodeCapture; error?: ScreenshotLifecycleError }>({ status: "idle" });
  const request = React.useCallback(() => {
    const controller = new AbortController();
    setState((previous) => ({ status: "loading", capture: previous.capture }));
    void requestNodeScreenshot(nodeId, { signal: controller.signal }).then(({ capture }) => {
      onCapture(capture);
      setState({ status: "ready", capture });
    }).catch((error: unknown) => {
      if (error instanceof ScreenshotLifecycleError && error.code === "cancelled") return;
      setState((previous) => ({ status: "error", capture: previous.capture, error: error instanceof ScreenshotLifecycleError ? error : new ScreenshotLifecycleError("http", "Screenshot refresh failed.") }));
    });
    return controller;
  }, [nodeId, onCapture]);
  const controller = React.useRef<AbortController | null>(null);
  const refresh = () => {
    controller.current?.abort();
    controller.current = request();
  };
  React.useEffect(() => () => {
    controller.current?.abort();
  }, []);
  return <div className="flex flex-wrap items-center gap-2">
    <Button variant="secondary" onClick={refresh} disabled={state.status === "loading"}>{state.status === "loading" ? "Capturing…" : state.capture ? "Refresh screenshot" : "Capture screenshot"}</Button>
    {state.capture?.capturedAt && <time className="text-xs text-muted-foreground" dateTime={state.capture.capturedAt}>Captured {new Date(state.capture.capturedAt).toLocaleString()}</time>}
    {state.error && <span role="alert" className="text-xs text-destructive">{screenshotFailureMessage[state.error.code as keyof typeof screenshotFailureMessage] ?? state.error.message}</span>}
  </div>;
}
function playbackMetadata(media: Media, previewUrl?: string): PlaybackMetadata {
  return {
    title: media.title,
    subtitle: media.kind === "web" ? media.url : media.artist || media.description,
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

async function resolveExpectedContent(nodeId: string): Promise<PlaybackMetadata | undefined> {
  let assignment: components["schemas"]["Assignment"];
  try {
    assignment = await getNodeAssignment(nodeId);
  } catch (error) {
    if (error instanceof ApiRequestError && error.status === 404) return undefined;
    throw error;
  }
  if (assignment.mediaId) {
    try {
      return playbackMetadata((await getMedia(assignment.mediaId)).media);
    } catch {
      return { title: "Assigned media", subtitle: "Media details are unavailable." };
    }
  }
  if (assignment.playlistId) {
    try {
      const playlist = readApiResult(await api.GET("/playlists/{playlistId}", { params: { path: { playlistId: assignment.playlistId } } }));
      return { title: `Playlist · ${playlist.name}`, subtitle: `${playlist.items.length} ${playlist.items.length === 1 ? "item" : "items"}` };
    } catch {
      return { title: "Assigned playlist", subtitle: "Playlist details are unavailable." };
    }
  }
  return { title: "Assigned content", subtitle: "Media preview unavailable" };
}


/** Resolves the observed and expected media previews for a screen, then hands the visual to `render`. */
function ScreenVisual({ node, report, capture, actions, render }: { node: Node; report?: NodeReport; capture?: NodeCapture; actions?: React.ReactNode; render: (visual: FleetNodeVisual) => React.ReactNode }) {
  const mediaIdentity = report?.currentContent?.mediaId || report?.currentContent?.assetId || "";
  const observed = useQuery({ queryKey: ["controller", "node-current-media", mediaIdentity], queryFn: () => resolveObservedMedia(report), enabled: Boolean(mediaIdentity) });
  const expected = useQuery({ queryKey: ["controller", "node-expected", node.id], queryFn: () => resolveExpectedContent(node.id), staleTime: 5_000, refetchInterval: 60_000 });
  const base: FleetNodeVisual = { node, report, capture, actions, lastContactAt: report?.reportedAt, nodeHref: `/screens/${node.id}` };
  return <AuthorizedPreview media={observed.data?.media} render={(observedUrl) => <AuthorizedPreview media={expected.data?.media} render={(expectedUrl) => render({
    ...base,
    observedNow: observed.data ? playbackMetadata(observed.data.media, observedUrl) : undefined,
    expectedNow: expected.data ? { ...expected.data, mediaPreviewUrl: expectedUrl } : undefined,
  })} />} />;
}

function AuthorizedPreview({ media, render }: { media?: Media; render: (url: string | undefined) => React.ReactNode }) {
  if (media) return <LoadedPreview media={media} render={render} />;
  return <>{render(undefined)}</>;
}

function LoadedPreview({ media, render }: { media: Media; render: (url: string | undefined) => React.ReactNode }) {
  const thumbnail = useMediaThumbnail(media);
  return <>{render(thumbnail.status === "ready" ? thumbnail.url : undefined)}</>;
}

const screenGrid = "grid gap-4 [grid-template-columns:repeat(auto-fill,minmax(min(100%,17rem),1fr))]";

/** Wall of screen tiles; shared by Overview and Screens. */
export function ScreenWall({ nodes, selectedIds, onSelectedChange }: { nodes: readonly Node[]; selectedIds?: readonly string[]; onSelectedChange?: (nodeId: string, selected: boolean) => void }) {
  const reports = useQueries({ queries: nodes.map((node) => ({ queryKey: queryKeys.nodeStatus(node.id), queryFn: () => getNodeStatus(node.id), staleTime: 5_000 })) });
  return <div role="list" aria-label="Screens" className={screenGrid}>{nodes.map((node, index) => <div role="listitem" key={node.id} className="min-w-0">
    <ScreenVisual node={node} report={reports[index]?.data ?? undefined} render={(visual) => <ScreenTile {...visual} selected={selectedIds?.includes(node.id)} onSelectedChange={onSelectedChange ? (selected) => onSelectedChange(node.id, selected) : undefined} />} />
  </div>)}</div>;
}

function BulkAssignmentComposer({ nodes, onClose }: { nodes: readonly Node[]; onClose: () => void }) {
  const client = useQueryClient();
  const [media, setMedia] = React.useState<components["schemas"]["Media"]>();
  const [deliveryMode, setDeliveryMode] = React.useState<DeliveryChoice>("");
  const mutation = useMutation({
    mutationFn: async () => {
      if (!media) throw new Error("Choose content before assigning.");
      for (const node of nodes) {
        const current = await api.GET("/nodes/{nodeId}/assignment", { params: { path: { nodeId: node.id } } });
        const etag = current.response.headers.get("ETag") ?? "*";
        readApiResult(await api.PUT("/nodes/{nodeId}/assignment", {
          params: { path: { nodeId: node.id }, header: { "If-Match": etag, "Idempotency-Key": crypto.randomUUID() } },
          body: { mediaId: media.id, deliveryMode: media.kind === "web" || !deliveryMode ? undefined : deliveryMode, expectedGeneration: node.desiredGeneration ?? 0, remoteInput: false, showQr: false },
        }));
      }
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["controller", "node-assignment"] });
      void client.invalidateQueries({ queryKey: ["controller", "node-expected"] });
      onClose();
    },
  });
  return <Card className="border-primary/50"><CardHeader><CardTitle>Assign content to {nodes.length} {nodes.length === 1 ? "screen" : "screens"}</CardTitle></CardHeader><CardContent className="grid gap-4 sm:grid-cols-[1fr_14rem_auto] sm:items-end">
    <MediaTargetField label="Content" value={media?.id} onChange={setMedia} disabled={mutation.isPending} />
    {media?.kind === "web" ? <span /> : <DeliveryField media={media} value={deliveryMode} onChange={setDeliveryMode} disabled={mutation.isPending} />}
    <div className="flex gap-2"><Button onClick={() => mutation.mutate()} disabled={mutation.isPending || !media}>{mutation.isPending ? "Assigning…" : "Assign"}</Button><Button variant="ghost" onClick={onClose} disabled={mutation.isPending}>Cancel</Button></div>
    {mutation.isError && <p role="alert" className="text-sm text-destructive sm:col-span-full">{mutation.error.message}</p>}
  </CardContent></Card>;
}

export function ScreensPage() {
  const search = useSearch({ from: "/screens" });
  const navigate = useNavigate({ from: "/screens" });
  const query = useQuery({ queryKey: queryKeys.nodes(search), queryFn: () => listNodes(search), placeholderData: keepPreviousData });
  const [selectedIds, setSelectedIds] = React.useState<string[]>([]);
  const [bulkOpen, setBulkOpen] = React.useState(false);
  const header = <PageHeader title="Screens" description="Every display on the network and what it is showing right now.">
    <Button asChild variant="secondary"><Link to="/screens/assign">Assign content</Link></Button>
    <Button asChild><Link to="/pair">Pair a screen</Link></Button>
  </PageHeader>;
  if (query.isPending) return <PageFrame>{header}<div className={screenGrid}>{[0, 1, 2].map((index) => <Skeleton key={index} className="aspect-video w-full" />)}</div></PageFrame>;
  if (query.isError) return <PageFrame>{header}<QueryError error={query.error} retry={() => void query.refetch()} /></PageFrame>;
  const items = query.data.items;
  const visibleSelection = selectedIds.filter((id) => items.some((node) => node.id === id));
  const selectedNodes = items.filter((node) => visibleSelection.includes(node.id));
  return <PageFrame>
    {header}
    <Toolbar>
      <SearchBox value={search.search ?? ""} onChange={(value) => navigate({ search: (previous) => ({ ...previous, search: value || undefined, cursor: undefined }) })} label="Search screens" />
      {items.length > 0 && <div className="flex items-center gap-3 text-sm" aria-label="Screen selection">
        <label className="inline-flex cursor-pointer items-center gap-2 text-muted-foreground"><input type="checkbox" aria-label="Select all screens" className="h-4 w-4 accent-primary" checked={visibleSelection.length === items.length} onChange={(event) => setSelectedIds(event.currentTarget.checked ? items.map((node) => node.id) : [])} />{visibleSelection.length ? `${visibleSelection.length} selected` : `${items.length} ${items.length === 1 ? "screen" : "screens"}`}</label>
        {visibleSelection.length > 0 && <Button size="sm" variant="secondary" aria-expanded={bulkOpen} onClick={() => setBulkOpen((open) => !open)}>Assign selected</Button>}
      </div>}
    </Toolbar>
    {bulkOpen && selectedNodes.length > 0 && <BulkAssignmentComposer nodes={selectedNodes} onClose={() => setBulkOpen(false)} />}
    {items.length
      ? <ScreenWall nodes={items} selectedIds={visibleSelection} onSelectedChange={(nodeId, selected) => setSelectedIds((previous) => nextNodeSelection(previous, nodeId, selected))} />
      : <EmptyState title={search.search ? "No matching screens" : "No screens yet"} description={search.search ? "Try a different name." : "Pair a TV browser with a code, or enroll the desktop node app."} action={<Button asChild><Link to="/pair">Pair a screen</Link></Button>} />}
    <Pagination page={query.data.page} onPrevious={() => navigate({ search: (previous) => ({ ...previous, cursor: query.data.page.previousCursor ?? undefined }) })} onNext={() => navigate({ search: (previous) => ({ ...previous, cursor: query.data.page.nextCursor ?? undefined }) })} />
  </PageFrame>;
}

export const screenTabs = [{ label: "Now playing", slug: "" }, { label: "Assignment", slug: "assignment" }, { label: "Remote", slug: "remote" }, { label: "Events", slug: "events" }, { label: "System", slug: "system" }] as const;
export type ScreenTab = typeof screenTabs[number]["slug"];
type NodeAssignment = components["schemas"]["Assignment"];

export function ScreenDetailPage({ nodeId, tab }: { nodeId: string; tab: ScreenTab }) {
  const node = useQuery({ queryKey: queryKeys.node(nodeId), queryFn: () => getNode(nodeId) });
  if (node.isPending) return <RoutePending />;
  if (node.isError) return <PageFrame><PageHeader title="Screen" /><QueryError error={node.error} retry={() => void node.refetch()} /></PageFrame>;
  const detail = node.data;
  return <PageFrame>
    <PageHeader eyebrow="Screen" title={detail.name} meta={<>
      <StatusBadge tone={detail.credentialState === "active" ? "success" : "danger"}>{detail.credentialState}</StatusBadge>
      <StatusBadge tone="neutral">{detail.nodeClass === "browser" ? "Browser node" : "Desktop node"}</StatusBadge>
      <code className="font-mono text-xs text-muted-foreground">{detail.id}</code>
    </>} />
    <SectionTabs label="Screen sections" linkComponent={RouterLink} tabs={screenTabs.map((item) => ({ label: item.label, href: `/screens/${nodeId}${item.slug ? `/${item.slug}` : ""}`, active: item.slug === tab }))} />
    {tab === "" && <NowPlayingPanel node={detail} />}
    {tab === "assignment" && <AssignmentPanel node={detail} />}
    {tab === "remote" && <RemotePanel node={detail} />}
    {tab === "events" && <EventsPanel nodeId={nodeId} />}
    {tab === "system" && <SystemPanel node={detail} />}
  </PageFrame>;
}

function NowPlayingPanel({ node }: { node: Node }) {
  const status = useQuery({ queryKey: queryKeys.nodeStatus(node.id), queryFn: () => getNodeStatus(node.id), refetchInterval: 15_000 });
  const [capture, setCapture] = React.useState<NodeCapture>();
  const onCapture = React.useCallback((next: NodeCapture) => setCapture(next), []);
  React.useEffect(() => () => disposeNodeCapture(capture), [capture?.url]);
  if (status.isPending) return <LoadingCard />;
  if (status.isError) return <QueryError error={status.error} retry={() => void status.refetch()} />;
  return <ScreenVisual node={node} report={status.data ?? undefined} capture={capture} actions={<NodeScreenshotControl nodeId={node.id} onCapture={onCapture} />} render={(visual) => <NodePlaybackView {...visual} />} />;
}

async function getOptionalAssignment(nodeId: string): Promise<NodeAssignment | null> {
  try {
    return await getNodeAssignment(nodeId);
  } catch (error) {
    if (error instanceof ApiRequestError && error.status === 404) return null;
    throw error;
  }
}

function AssignmentPanel({ node }: { node: Node }) {
  const assignment = useQuery({ queryKey: queryKeys.nodeAssignment(node.id), queryFn: () => getOptionalAssignment(node.id) });
  const expected = useQuery({ queryKey: ["controller", "node-expected", node.id], queryFn: () => resolveExpectedContent(node.id), staleTime: 5_000 });
  return <div className="grid items-start gap-5 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)]">
    <Card><CardHeader><CardTitle>Current assignment</CardTitle></CardHeader><CardContent>
      {assignment.isPending ? <Skeleton className="h-24 w-full" /> : assignment.isError ? <QueryError error={assignment.error} retry={() => void assignment.refetch()} /> : assignment.data ? <Facts items={[
        { label: "Content", value: expected.data ? <span className="block min-w-0">{assignment.data.mediaId ? <Link className="block font-medium hover:text-primary" to="/library/media/$mediaId" params={{ mediaId: assignment.data.mediaId }}>{expected.data.title}</Link> : <span className="block font-medium">{expected.data.title}</span>}{expected.data.subtitle && <span className="block break-all text-xs text-muted-foreground">{expected.data.subtitle}</span>}</span> : "…", wide: true },
        { label: "Delivery", value: assignment.data.deliveryMode === "cache_then_play" ? "Cache then play" : "Stream" },
        { label: "Release", value: assignment.data.releaseId ? <Link className="text-primary hover:underline" to="/releases/$releaseId" params={{ releaseId: assignment.data.releaseId }}>{shortId(assignment.data.releaseId)}</Link> : "—" },
        { label: "Generation", value: assignment.data.desiredGeneration },
        { label: "Remote control", value: assignment.data.remoteInput ? "Allowed" : "Off" },
        { label: "QR on screen", value: assignment.data.remoteInput && assignment.data.showQr ? "Shown" : "Hidden" },
        { label: "Release digest", value: assignment.data.releaseDigest ?? "—", mono: true, wide: true },
      ]} /> : <EmptyState title="Nothing assigned" description="This screen has no assignment yet. Choose content on the right." />}
    </CardContent></Card>
    <NodeAssignmentComposer fixedNodeId={node.id} title="Change assignment" />
  </div>;
}

function RemotePanel({ node }: { node: Node }) {
  const assignment = useQuery({ queryKey: queryKeys.nodeAssignment(node.id), queryFn: () => getOptionalAssignment(node.id) });
  const link = useMutation({ mutationFn: async () => readApiResult(await api.POST("/nodes/{nodeId}/remote-link", { params: { path: { nodeId: node.id } } })) });
  const [qr, setQr] = React.useState<string>();
  const [copied, setCopied] = React.useState(false);
  React.useEffect(() => {
    setQr(undefined);
    if (!link.data) return;
    let live = true;
    void QRCode.toDataURL(link.data.remoteUrl, { margin: 1, width: 360, errorCorrectionLevel: "M" }).then((url) => { if (live) setQr(url); });
    return () => { live = false; };
  }, [link.data]);
  const remoteAllowed = assignment.data?.remoteInput === true;
  const status = link.error instanceof ApiRequestError ? link.error.status : undefined;
  return <div className="grid items-start gap-5 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
    <Card><CardHeader><CardTitle>Phone remote</CardTitle></CardHeader><CardContent className="space-y-4">
      <p className="text-sm text-muted-foreground">Open a remote control for this screen on any phone. The link works until the screen's remote session ends; anyone holding it can send input.</p>
      {assignment.isPending ? <Skeleton className="h-6 w-48" /> : assignment.isError ? <QueryError error={assignment.error} retry={() => void assignment.refetch()} /> : <Facts items={[
        { label: "Remote control", value: remoteAllowed ? <StatusBadge tone="success">Allowed</StatusBadge> : <StatusBadge tone="neutral">Off</StatusBadge> },
        { label: "QR on screen", value: remoteAllowed && assignment.data?.showQr ? "Shown" : "Hidden" },
      ]} />}
      {!assignment.isPending && !remoteAllowed && <p className="rounded-md border border-status-warning/40 bg-status-warning/10 p-3 text-sm">Remote control is off for this screen. Turn on <strong>Allow remote control</strong> in <Link className="font-medium text-primary hover:underline" to="/screens/$nodeId/assignment" params={{ nodeId: node.id }}>Assignment</Link>.</p>}
      <Button onClick={() => link.mutate()} disabled={link.isPending}>{link.isPending ? "Creating link…" : link.data ? "Create a new link" : "Create phone link"}</Button>
      {link.isError && <ErrorState title={status === 404 ? "No active remote session" : status === 409 ? "Remote control is off" : "Link unavailable"} description={status === 404 ? "The screen has not opened a remote session yet. Make sure it is online and remote control is allowed, then try again." : status === 409 ? "Allow remote control in this screen's assignment first." : link.error.message} />}
    </CardContent></Card>
    {link.data && <Card><CardHeader><CardTitle>Scan to control {node.name}</CardTitle></CardHeader><CardContent className="space-y-4">
      <div className="mx-auto w-full max-w-[16rem] rounded-lg bg-white p-3">{qr ? <img src={qr} alt={`QR code for the ${node.name} phone remote`} className="aspect-square w-full [image-rendering:pixelated]" /> : <Skeleton className="aspect-square w-full" />}</div>
      <div className="flex gap-2"><input aria-label="Phone remote link" readOnly value={link.data.remoteUrl} className="h-9 min-w-0 flex-1 rounded-md border border-input bg-card px-3 font-mono text-xs" onFocus={(event) => event.currentTarget.select()} /><Button variant="secondary" onClick={() => void navigator.clipboard.writeText(link.data.remoteUrl).then(() => setCopied(true))}>{copied ? "Copied" : "Copy"}</Button><Button variant="secondary" asChild><a href={link.data.remoteUrl} target="_blank" rel="noreferrer">Open</a></Button></div>
      <p className="text-xs text-muted-foreground">Expires <time dateTime={link.data.expiresAt}>{formatWhen(link.data.expiresAt)}</time></p>
    </CardContent></Card>}
  </div>;
}

function EventsPanel({ nodeId }: { nodeId: string }) {
  const events = useQuery({ queryKey: queryKeys.nodeEvents(nodeId), queryFn: () => listNodeEvents(nodeId) });
  if (events.isPending) return <LoadingCard />;
  if (events.isError) return <QueryError error={events.error} retry={() => void events.refetch()} />;
  return <TanStackDataTable caption="Screen events" rows={[...events.data.items].sort((a, b) => b.occurredAt.localeCompare(a.occurredAt))} rowKey={(event) => event.id} empty={<EmptyState title="No events" description="The controller has not recorded events for this screen." />} columns={[
    { key: "occurred", header: "When", render: (event) => <time className="whitespace-nowrap" dateTime={event.occurredAt}>{formatWhen(event.occurredAt)}</time> },
    { key: "type", header: "Event", render: (event) => <code className="font-mono text-xs">{event.type}</code> },
    { key: "sequence", header: "Seq", render: (event) => <span className="font-mono text-xs tabular-nums text-muted-foreground">{event.sequence}</span> },
  ]} />;
}

function SystemPanel({ node }: { node: Node }) {
  const auth = useAuth();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const capabilities = useQuery({ queryKey: queryKeys.nodeCapabilities(node.id), queryFn: () => getNodeCapabilities(node.id) });
  const status = useQuery({ queryKey: queryKeys.nodeStatus(node.id), queryFn: () => getNodeStatus(node.id) });
  const [confirming, setConfirming] = React.useState<"unregister" | "revoke">();
  const removal = useMutation({
    mutationFn: async (action: "unregister" | "revoke") => {
      if (action === "revoke") {
        const result = await api.POST("/browser-nodes/{nodeId}/revoke", { params: { path: { nodeId: node.id } } });
        if (result.error) throw new ApiRequestError(result.response.status, "The browser credential could not be revoked.");
        return;
      }
      const result = await api.DELETE("/nodes/{nodeId}", { params: { path: { nodeId: node.id }, header: { "Idempotency-Key": crypto.randomUUID() } } });
      if (result.error) throw new ApiRequestError(result.response.status, "The screen could not be unregistered.");
    },
    onSuccess: (_, action) => {
      void queryClient.invalidateQueries({ queryKey: ["controller", "nodes"] });
      void queryClient.invalidateQueries({ queryKey: queryKeys.node(node.id) });
      if (action === "unregister") void navigate({ to: "/screens" });
      setConfirming(undefined);
    },
  });
  const isAdmin = hasScope(auth.operator, "security_admin");
  return <div className="grid items-start gap-5 lg:grid-cols-2">
    <Card><CardHeader><CardTitle>Capabilities</CardTitle></CardHeader><CardContent>
      {capabilities.isPending ? <Skeleton className="h-24 w-full" /> : capabilities.isError ? (capabilities.error instanceof ApiRequestError && capabilities.error.status === 404 ? <p className="text-sm text-muted-foreground">This screen has not reported its capabilities yet.</p> : <QueryError error={capabilities.error} retry={() => void capabilities.refetch()} />) : <Facts items={[
        { label: "Platform", value: `${capabilities.data.platform} / ${capabilities.data.architecture}` },
        { label: "Protocol", value: capabilities.data.protocolVersion },
        { label: "Renderer", value: capabilities.data.rendererVersion },
        { label: "Profile", value: capabilities.data.profileId },
        { label: "Features", value: capabilities.data.features?.join(", ") || "None", wide: true },
        { label: "Media types", value: capabilities.data.acceptedMediaTypes?.join(", ") || "None", wide: true },
      ]} />}
    </CardContent></Card>
    <Card><CardHeader><CardTitle>Cache activation</CardTitle></CardHeader><CardContent>
      {status.isPending ? <Skeleton className="h-16 w-full" /> : status.isError ? <QueryError error={status.error} retry={() => void status.refetch()} /> : status.data ? <Facts columns={1} items={[
        { label: "Candidate release", value: status.data.candidateReleaseDigest ?? "None", mono: true },
        { label: "Last known good", value: status.data.lastKnownGoodReleaseDigest ?? "—", mono: true },
        { label: "Last report", value: formatWhen(status.data.reportedAt) },
      ]} /> : <p className="text-sm text-muted-foreground">This screen has not reported yet.</p>}
    </CardContent></Card>
    {isAdmin && <Card className="border-status-danger/40 lg:col-span-2"><CardHeader><CardTitle className="text-status-danger">Danger zone</CardTitle></CardHeader><CardContent className="space-y-4">
      {node.nodeClass === "browser" && <DangerRow title="Revoke browser credential" description="The TV forgets its pairing and shows a new code. The screen stays listed as revoked." confirming={confirming === "revoke"} pending={removal.isPending} onAsk={() => setConfirming("revoke")} onCancel={() => setConfirming(undefined)} onConfirm={() => removal.mutate("revoke")} label="Revoke" />}
      <DangerRow title="Unregister screen" description="Removes the screen and cuts off its credentials. It must be paired or enrolled again." confirming={confirming === "unregister"} pending={removal.isPending} onAsk={() => setConfirming("unregister")} onCancel={() => setConfirming(undefined)} onConfirm={() => removal.mutate("unregister")} label="Unregister screen" />
      {removal.isError && <QueryError error={removal.error} retry={() => removal.reset()} />}
    </CardContent></Card>}
  </div>;
}

function DangerRow({ title, description, label, confirming, pending, onAsk, onCancel, onConfirm }: { title: string; description: string; label: string; confirming: boolean; pending: boolean; onAsk: () => void; onCancel: () => void; onConfirm: () => void }) {
  return <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border pt-4 first:border-t-0 first:pt-0">
    <div className="min-w-0"><p className="font-medium">{title}</p><p className="text-sm text-muted-foreground">{description}</p></div>
    {confirming ? <div className="flex gap-2"><Button variant="ghost" onClick={onCancel} disabled={pending}>Cancel</Button><Button variant="danger" onClick={onConfirm} disabled={pending}>{pending ? "Working…" : `Confirm ${label.toLocaleLowerCase()}`}</Button></div> : <Button variant="danger" onClick={onAsk}>{label}</Button>}
  </div>;
}

export function AssignPage() {
  return <PageFrame width="narrow"><PageHeader title="Assign content" description="Choose what to show — an image, video, or web page — then pick the screen." /><NodeAssignmentComposer /></PageFrame>;
}

/** Normalises a pairing code the way the controller does: case, spaces, and dashes do not matter. */
export function normalizePairingCode(value: string): string {
  return value.toUpperCase().replace(/[^A-Z0-9]/g, "");
}

export function PairPage() {
  const search = useSearch({ from: "/pair" });
  const auth = useAuth();
  const client = useQueryClient();
  const [code, setCode] = React.useState(search.code ?? "");
  const [name, setName] = React.useState("");
  React.useEffect(() => { if (search.code) setCode(search.code); }, [search.code]);
  const claim = useMutation({
    mutationFn: async () => readApiResult(await api.POST("/browser-nodes/claims", { body: { code: normalizePairingCode(code), name: name.trim() } })),
    onSuccess: () => void client.invalidateQueries({ queryKey: ["controller", "nodes"] }),
  });
  const enrollment = useMutation({ mutationFn: async () => readApiResult(await api.POST("/enrollment-tokens", { params: { header: { "Idempotency-Key": crypto.randomUUID() } } })) });
  const canEnroll = hasScope(auth.operator, "node_enroll");
  const claimStatus = claim.error instanceof ApiRequestError ? claim.error.status : undefined;
  const origin = typeof window === "undefined" ? "" : window.location.origin;
  return <PageFrame width="narrow">
    <PageHeader eyebrow="Screens" title="Pair a screen" description="Connect a TV or any web browser with a short code, or enroll the desktop node app." />
    <Card><CardHeader><CardTitle>Browser screen</CardTitle></CardHeader><CardContent className="space-y-5">
      <ol className="grid gap-3 text-sm sm:grid-cols-3">
        <li className="rounded-md border border-border p-3"><span className="eyebrow text-primary">1</span><p className="mt-1">On the screen, open <code className="break-all font-mono text-xs">{origin}/node/</code></p></li>
        <li className="rounded-md border border-border p-3"><span className="eyebrow text-primary">2</span><p className="mt-1">It shows a code and a QR. Scanning the QR opens this page with the code filled in.</p></li>
        <li className="rounded-md border border-border p-3"><span className="eyebrow text-primary">3</span><p className="mt-1">Name the screen and pair. It starts playing within seconds.</p></li>
      </ol>
      {claim.isSuccess ? <div role="status" className="space-y-3 rounded-md border border-status-success/40 bg-status-success/10 p-4">
        <p className="font-medium">Paired “{claim.data.name}”.</p>
        <div className="flex flex-wrap gap-2"><Button asChild><Link to="/screens/$nodeId/assignment" params={{ nodeId: claim.data.id }}>Choose what it shows</Link></Button><Button asChild variant="secondary"><Link to="/screens/$nodeId" params={{ nodeId: claim.data.id }}>Open screen</Link></Button><Button variant="ghost" onClick={() => { claim.reset(); setCode(""); setName(""); }}>Pair another</Button></div>
      </div> : <form className="grid gap-4 sm:grid-cols-2" onSubmit={(event) => { event.preventDefault(); claim.mutate(); }}>
        <label className="block text-sm font-medium">Pairing code<input required autoComplete="off" autoCapitalize="characters" spellCheck={false} className={`${fieldClass} font-mono text-lg uppercase tracking-[0.2em]`} placeholder="K7QM-4XPD" value={code} onChange={(event) => setCode(event.currentTarget.value)} /></label>
        <label className="block text-sm font-medium">Screen name<input required maxLength={120} className={fieldClass} placeholder="Lobby TV" value={name} onChange={(event) => setName(event.currentTarget.value)} /></label>
        {claim.isError && <div className="sm:col-span-2"><ErrorState title={claimStatus === 404 ? "Code not found" : claimStatus === 409 ? "Already paired" : claimStatus === 403 ? "Not allowed" : "Pairing failed"} description={claimStatus === 404 ? "The code is wrong or has expired. Codes last 10 minutes — check the screen for a fresh one." : claimStatus === 409 ? "Another operator already paired this code." : claimStatus === 403 ? "Your operator token cannot enroll screens." : claim.error.message} /></div>}
        <div className="sm:col-span-2"><Button type="submit" disabled={claim.isPending || normalizePairingCode(code).length < 4 || !name.trim()}>{claim.isPending ? "Pairing…" : "Pair screen"}</Button></div>
      </form>}
    </CardContent></Card>
    {canEnroll && <Card><CardHeader><CardTitle>Desktop node app</CardTitle></CardHeader><CardContent className="space-y-3">
      <p className="text-sm text-muted-foreground">Create a one-time enrollment token and enter it in Cuelight. It cannot be retrieved again.</p>
      {enrollment.data ? <><div className="flex gap-2"><input aria-label="Enrollment token" readOnly value={enrollment.data.secret} className="h-9 min-w-0 flex-1 rounded-md border border-input bg-card px-3 font-mono text-xs" onFocus={(event) => event.currentTarget.select()} /><Button variant="secondary" onClick={() => void navigator.clipboard.writeText(enrollment.data.secret)}>Copy</Button></div><p className="text-xs text-muted-foreground">Expires <time dateTime={enrollment.data.expiresAt}>{formatWhen(enrollment.data.expiresAt)}</time></p></> : <Button variant="secondary" onClick={() => enrollment.mutate()} disabled={enrollment.isPending}>{enrollment.isPending ? "Creating token…" : "Create enrollment token"}</Button>}
      {enrollment.isError && <QueryError error={enrollment.error} retry={() => enrollment.reset()} />}
    </CardContent></Card>}
  </PageFrame>;
}
