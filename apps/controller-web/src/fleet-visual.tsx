import * as React from "react";
import type { components } from "@cable/api-client";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  EmptyState,
  NowPlayingCard,
  PreviewMosaic,
  PreviewSurface,
  StatusSummary,
  TechnicalDetails,
  humanizeStatus,
  type PreviewMedia,
  type StatusTone,
  type TechnicalDetail,
} from "@cable/controller-ui";

type Node = components["schemas"]["Node"];
export type NodeReport = components["schemas"]["NodeReport"];
export type CurrentContent = components["schemas"]["CurrentContent"];
export type Media = components["schemas"]["Media"];
type CapabilityReport = components["schemas"]["CapabilityReport"];

export interface PlaybackMetadata {
  title: string;
  subtitle?: string;
  media?: Media;
  /** An already-authorized object URL for the supplied media. */
  mediaPreviewUrl?: string;
  channelName?: string;
  blockTitle?: string;
  startsAt?: string;
  endsAt?: string;
  durationMs?: number;
}

export interface NodeCapture {
  /** An already-authorized object URL for a screenshot captured from the node. */
  url: string;
  capturedAt?: string;
  alt?: string;
}

export interface FleetNodeVisual {
  node: Node;
  report?: NodeReport;
  lastContactAt?: string;
  /** A controller-level health value such as online, degraded, or offline. */
  health?: string;
  healthDetail?: string;
  observedNow?: PlaybackMetadata;
  expectedNow?: PlaybackMetadata;
  next?: PlaybackMetadata;
  capture?: NodeCapture;
  capabilities?: CapabilityReport;
  nodeHref?: string;
  actions?: React.ReactNode;
}

export interface FleetNodeCardProps extends FleetNodeVisual {
  selected?: boolean;
  onSelectedChange?: (selected: boolean) => void;
  now?: Date | number;
}

export interface FleetNodeListProps {
  nodes: readonly FleetNodeVisual[];
  selectedNodeIds?: readonly string[];
  onSelectedNodeIdsChange?: (nodeIds: string[]) => void;
  bulkActions?: React.ReactNode | ((selectedNodes: readonly FleetNodeVisual[]) => React.ReactNode);
  empty?: React.ReactNode;
  now?: Date | number;
}

export interface NodePlaybackViewProps extends FleetNodeVisual {
  now?: Date | number;
}

interface HealthSummary {
  status: string;
  label: string;
  detail?: string;
  tone: StatusTone;
}

function toneForPlayback(state: CurrentContent["state"]): StatusTone {
  if (state === "playing") return "success";
  if (state === "paused" || state === "stale") return "warning";
  return "neutral";
}

function nodeHealth({ report, health, healthDetail }: Pick<FleetNodeVisual, "report" | "health" | "healthDetail">): HealthSummary {
  if (health) {
    return {
      status: health,
      label: humanizeStatus(health),
      detail: healthDetail,
      tone: /offline|failed|unhealthy|error/i.test(health) ? "danger" : /stale|degraded|warning/i.test(health) ? "warning" : /online|healthy|connected/i.test(health) ? "success" : "neutral",
    };
  }

  const failedCondition = report?.conditions.find((condition) =>
    condition.type === "SafeMode" ? condition.status : !condition.status
  );
  if (failedCondition) {
    return {
      status: "needs_attention",
      label: "Needs attention",
      detail: humanizeStatus(failedCondition.reason || failedCondition.type),
      tone: "danger",
    };
  }

  const observed = report?.currentContent;
  if (!observed) return { status: "unobserved", label: "Observation unavailable", tone: "neutral" };
  if (observed.state === "playing") return { status: observed.state, label: "Playing", tone: "success" };
  if (observed.state === "off_air") return { status: observed.state, label: "Off air", tone: "neutral" };
  return { status: observed.state, label: humanizeStatus(observed.state), tone: toneForPlayback(observed.state) };
}

function relativeUnit(value: number, unit: string): string {
  return `${value} ${unit}${value === 1 ? "" : "s"}`;
}

export function formatLastContact(value: string | undefined, now: Date | number = Date.now()): string {
  if (!value) return "Contact time unavailable";
  const contact = new Date(value).valueOf();
  const reference = now instanceof Date ? now.valueOf() : now;
  if (!Number.isFinite(contact) || !Number.isFinite(reference)) return "Contact time unavailable";

  const elapsedSeconds = Math.round((reference - contact) / 1_000);
  const future = elapsedSeconds < -5;
  const seconds = Math.abs(elapsedSeconds);
  if (seconds < 45) return "Just now";
  const phrase = seconds < 3_600
    ? relativeUnit(Math.round(seconds / 60), "minute")
    : seconds < 86_400
      ? relativeUnit(Math.round(seconds / 3_600), "hour")
      : relativeUnit(Math.round(seconds / 86_400), "day");
  return future ? `In ${phrase}` : `${phrase} ago`;
}

export function nextNodeSelection(selectedNodeIds: readonly string[], nodeId: string, selected: boolean): string[] {
  const next = new Set(selectedNodeIds);
  if (selected) next.add(nodeId);
  else next.delete(nodeId);
  return [...next];
}

function formatDateTime(value: string | undefined): string | null {
  if (!value) return null;
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? null : date.toLocaleString();
}

function LastContact({ value, now }: { value?: string; now?: Date | number }) {
  const label = formatLastContact(value, now);
  const absolute = formatDateTime(value);
  return value && absolute
    ? <time className="text-xs text-muted-foreground" dateTime={value} title={absolute}>Last contact {label.toLocaleLowerCase()}</time>
    : <span className="text-xs text-muted-foreground">{label}</span>;
}

function mediaPreview(metadata: PlaybackMetadata | undefined): PreviewMedia | undefined {
  if (!metadata?.media || !metadata.mediaPreviewUrl) return undefined;
  if (metadata.media.artifact.mediaType.startsWith("video/")) {
    return { kind: "video", src: metadata.mediaPreviewUrl, label: `Preview of ${metadata.title}`, controls: true };
  }
  return { kind: "image", src: metadata.mediaPreviewUrl, alt: `Preview of ${metadata.title}` };
}

function CapturePreview({ nodeName, capture, compact = false }: { nodeName: string; capture?: NodeCapture; compact?: boolean }) {
  return <PreviewSurface
    aria-label={`Actual capture for ${nodeName}`}
    media={capture ? { kind: "image", src: capture.url, alt: capture.alt ?? `Actual capture from ${nodeName}` } : undefined}
    state={capture ? undefined : "empty"}
    className={compact ? "rounded-none border-x-0" : undefined}
    emptyTitle="No capture exists"
    emptyDescription="The node has not supplied a screenshot. No image is inferred from its playback report."
    overlay={capture?.capturedAt ? <time dateTime={capture.capturedAt}>Captured {formatDateTime(capture.capturedAt) ?? "at an unknown time"}</time> : undefined}
  />;
}

function ExpectedPreview({ expected, compact = false }: { expected?: PlaybackMetadata; compact?: boolean }) {
  const preview = mediaPreview(expected);
  return <PreviewSurface
    aria-label="Expected media preview"
    media={preview}
    state={preview ? undefined : "empty"}
    className={compact ? "rounded-none border-x-0" : undefined}
    fit={expected?.media?.playback.fit === "fill" ? "cover" : "contain"}
    emptyTitle={expected ? "Expected preview unavailable" : "No expected content"}
    emptyDescription={expected ? "Expected metadata is available, but no media preview URL was supplied." : "No current schedule or assignment metadata was supplied."}
  />;
}

function MetadataSummary({ metadata, empty }: { metadata?: PlaybackMetadata; empty: string }) {
  if (!metadata) return <p className="text-sm text-muted-foreground">{empty}</p>;
  return <div className="min-w-0">
    <p className="truncate text-sm font-medium text-foreground">{metadata.title}</p>
    {metadata.subtitle && <p className="mt-1 truncate text-xs text-muted-foreground">{metadata.subtitle}</p>}
    {(metadata.channelName || metadata.blockTitle) && <p className="mt-1 truncate text-xs text-muted-foreground">
      {[metadata.channelName, metadata.blockTitle].filter(Boolean).join(" · ")}
    </p>}
  </div>;
}

function ObservedIdentity({ report, observedNow }: { report?: NodeReport; observedNow?: PlaybackMetadata }) {
  const observed = report?.currentContent;
  if (!observed) return <div className="rounded-md border border-dashed border-border p-4">
    <p className="text-sm font-medium">Playback observation unavailable</p>
    <p className="mt-1 text-xs text-muted-foreground">This node has not reported current content. It may be an older player or have no recent observation.</p>
  </div>;

  const media = observedNow?.title ?? (observed.mediaId ? "Media active" : observed.state === "off_air" ? "No media on air" : "Media not reported");
  const channel = observedNow?.channelName ?? (observed.channelId ? "Channel active" : "Channel not reported");
  const block = observedNow?.blockTitle ?? (observed.blockId ? "Block active" : "Block not reported");
  return <dl className="grid gap-3 text-sm sm:grid-cols-3">
    <div><dt className="text-xs text-muted-foreground">Current media</dt><dd className="mt-1 font-medium">{media}</dd></div>
    <div><dt className="text-xs text-muted-foreground">Channel</dt><dd className="mt-1">{channel}</dd></div>
    <div><dt className="text-xs text-muted-foreground">Block</dt><dd className="mt-1">{block}</dd></div>
  </dl>;
}

function technicalItems(node: Node, report?: NodeReport, capabilities?: CapabilityReport): TechnicalDetail[] {
  const observed = report?.currentContent;
  const items: TechnicalDetail[] = [
    { label: "Node ID", value: <code className="font-mono text-xs">{node.id}</code> },
    { label: "Credential state", value: humanizeStatus(node.credentialState) },
  ];
  if (node.desiredGeneration !== undefined) items.push({ label: "Desired generation", value: node.desiredGeneration.toLocaleString() });
  if (node.observedGeneration !== undefined) items.push({ label: "Inventory observed generation", value: node.observedGeneration.toLocaleString() });
  if (!report) return items;

  items.push(
    { label: "Event epoch", value: <code className="break-all font-mono text-xs">{report.eventEpoch}</code> },
    { label: "Event sequence", value: report.sequence.toLocaleString() },
    { label: "Desired generation seen", value: report.desiredGenerationSeen.toLocaleString() },
    { label: "Reported observed generation", value: report.observedGeneration.toLocaleString() },
    { label: "Presentation protocol", value: humanizeStatus(report.presentationKind ?? "not_reported") },
  );

  const digests: Array<[string, string | null | undefined]> = [
    ["Current content release digest", observed?.releaseDigest],
    ["Rendering release digest", report.renderingReleaseDigest],
    ["Candidate release digest", report.candidateReleaseDigest],
    ["Healthy release digest", report.healthyReleaseDigest],
    ["Last known good release digest", report.lastKnownGoodReleaseDigest],
  ];
  for (const [label, digest] of digests) {
    if (digest) items.push({ label, value: <code className="break-all font-mono text-xs">{digest}</code> });
  }

  if (observed) {
    const ids: Array<[string, string | undefined]> = [
      ["Observed channel ID", observed.channelId],
      ["Observed block ID", observed.blockId],
      ["Observed item ID", observed.itemId],
      ["Observed media ID", observed.mediaId],
      ["Observed asset ID", observed.assetId],
    ];
    for (const [label, id] of ids) if (id) items.push({ label, value: <code className="break-all font-mono text-xs">{id}</code> });
    items.push({ label: "Observed transition", value: formatDateTime(observed.transitionedAt) ?? observed.transitionedAt });
  }

  if (capabilities) {
    items.push(
      { label: "Protocol version", value: capabilities.protocolVersion },
      { label: "Renderer version", value: capabilities.rendererVersion },
      { label: "Platform", value: `${capabilities.platform} / ${capabilities.architecture}` },
      { label: "Renderer profile", value: capabilities.profileId },
    );
    if (capabilities.features?.length) items.push({ label: "Protocol features", value: capabilities.features.join(", ") });
    if (capabilities.acceptedMediaTypes?.length) items.push({ label: "Accepted media types", value: capabilities.acceptedMediaTypes.join(", ") });
  }
  return items;
}

function Conditions({ report }: { report?: NodeReport }) {
  if (!report) return null;
  return <div>
    <h4 className="text-xs font-medium text-muted-foreground">Raw conditions</h4>
    {report.conditions.length ? <ul className="mt-2 space-y-2">
      {report.conditions.map((condition, index) => <li key={`${condition.type}-${index}`} className="rounded-md border border-border bg-background p-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <code className="font-mono text-xs">{condition.type}</code>
          <span className={condition.status ? "text-status-success" : "text-status-danger"}>{condition.status ? "True" : "False"}</span>
        </div>
        <p className="mt-1 text-xs text-muted-foreground">{condition.reason}{condition.message ? ` · ${condition.message}` : ""}</p>
      </li>)}
    </ul> : <p className="mt-2 text-xs text-muted-foreground">No conditions reported.</p>}
  </div>;
}

function NodeTechnicalDetails({ node, report, capabilities }: Pick<FleetNodeVisual, "node" | "report" | "capabilities">) {
  return <TechnicalDetails items={technicalItems(node, report, capabilities)}><Conditions report={report} /></TechnicalDetails>;
}

function SelectionControl({ nodeName, selected, onSelectedChange }: { nodeName: string; selected: boolean; onSelectedChange: (selected: boolean) => void }) {
  return <label className="inline-flex cursor-pointer items-center gap-2 rounded-md text-xs font-medium text-muted-foreground focus-within:text-foreground">
    <input
      type="checkbox"
      className="h-4 w-4 rounded border-input accent-primary"
      checked={selected}
      onChange={(event) => onSelectedChange(event.currentTarget.checked)}
    />
    <span>Select {nodeName}</span>
  </label>;
}

export function FleetNodeCard({
  node,
  report,
  lastContactAt,
  health,
  healthDetail,
  observedNow,
  expectedNow,
  next,
  capture,
  capabilities,
  nodeHref,
  actions,
  selected = false,
  onSelectedChange,
  now,
}: FleetNodeCardProps) {
  const summary = nodeHealth({ report, health, healthDetail });
  const expectedHeadingId = React.useId();
  const captureHeadingId = React.useId();
  return <article
    aria-label={`Node ${node.name}`}
    data-selected={selected || undefined}
    className={`overflow-hidden rounded-lg border bg-card text-card-foreground shadow-sm transition-colors ${selected ? "border-primary ring-2 ring-ring/20" : "border-border"}`}
  >
    <div className="space-y-4 p-4">
      <header className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <h2 className="truncate text-base font-semibold tracking-tight">
            {nodeHref ? <a className="rounded-sm hover:text-primary hover:underline" href={nodeHref}>{node.name}</a> : node.name}
          </h2>
          <div className="mt-1"><LastContact value={lastContactAt} now={now} /></div>
        </div>
        {onSelectedChange && <SelectionControl nodeName={node.name} selected={selected} onSelectedChange={onSelectedChange} />}
      </header>
      <StatusSummary status={summary.status} label={summary.label} detail={summary.detail} tone={summary.tone} ariaLive="polite" />
      <ObservedIdentity report={report} observedNow={observedNow} />
    </div>

    <div className={`grid border-y border-border ${capture ? "sm:grid-cols-2 sm:divide-x sm:divide-border" : ""}`}>
      <section aria-labelledby={expectedHeadingId} className="min-w-0">
        <h3 id={expectedHeadingId} className="border-b border-border px-4 py-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">Preview</h3>
        <ExpectedPreview expected={expectedNow} compact />
        <div className="p-4"><MetadataSummary metadata={expectedNow} empty="Preview unavailable." /></div>
      </section>
      {capture && <section aria-labelledby={captureHeadingId} className="min-w-0 border-t border-border sm:border-t-0">
        <h3 id={captureHeadingId} className="border-b border-border px-4 py-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">Actual capture</h3>
        <CapturePreview nodeName={node.name} capture={capture} compact />
      </section>}
    </div>

    <div className="space-y-4 p-4">
      {next && <section aria-label="Up next" className="rounded-md bg-muted/40 p-3">
        <p className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">Up next</p>
        <MetadataSummary metadata={next} empty="No next item supplied." />
      </section>}
      <NodeTechnicalDetails node={node} report={report} capabilities={capabilities} />
      {actions && <div className="flex flex-wrap items-center gap-2 border-t border-border pt-4" aria-label={`${node.name} actions`}>{actions}</div>}
    </div>
  </article>;
}

export function FleetNodeList({
  nodes,
  selectedNodeIds = [],
  onSelectedNodeIdsChange,
  bulkActions,
  empty,
  now,
}: FleetNodeListProps) {
  const selectedSet = new Set(selectedNodeIds);
  const selectedNodes = nodes.filter((item) => selectedSet.has(item.node.id));
  const allSelected = nodes.length > 0 && selectedNodes.length === nodes.length;
  const someSelected = selectedNodes.length > 0 && !allSelected;
  const selectAll = React.useRef<HTMLInputElement>(null);
  React.useEffect(() => {
    if (selectAll.current) selectAll.current.indeterminate = someSelected;
  }, [someSelected]);

  if (!nodes.length) return <>{empty ?? <EmptyState title="No nodes to show" description="No registered nodes match the current fleet view." />}</>;

  const setSelection = (nodeId: string, selected: boolean) => {
    onSelectedNodeIdsChange?.(nextNodeSelection(selectedNodeIds, nodeId, selected));
  };
  const setAll = (selected: boolean) => {
    onSelectedNodeIdsChange?.(selected ? nodes.map((item) => item.node.id) : []);
  };
  const renderedBulkActions = typeof bulkActions === "function" ? bulkActions(selectedNodes) : bulkActions;

  return <div className="space-y-4">
    {(onSelectedNodeIdsChange || bulkActions) && <div className="flex flex-col justify-between gap-3 rounded-lg border border-border bg-card p-3 sm:flex-row sm:items-center" aria-label="Fleet selection and bulk actions">
      <div className="flex items-center gap-3">
        {onSelectedNodeIdsChange && <label className="inline-flex cursor-pointer items-center gap-2 text-sm font-medium">
          <input
            ref={selectAll}
            type="checkbox"
            className="h-4 w-4 rounded border-input accent-primary"
            aria-label="Select all nodes"
            checked={allSelected}
            onChange={(event) => setAll(event.currentTarget.checked)}
          />
          Select all
        </label>}
        <span className="text-sm text-muted-foreground" aria-live="polite">{selectedNodes.length.toLocaleString()} selected</span>
      </div>
      {renderedBulkActions && <div className="flex flex-wrap items-center gap-2" aria-label="Bulk node actions">{renderedBulkActions}</div>}
    </div>}
    <PreviewMosaic label="Fleet nodes" columns={2}>
      {nodes.map((item) => <FleetNodeCard
        key={item.node.id}
        {...item}
        now={now}
        selected={selectedSet.has(item.node.id)}
        onSelectedChange={onSelectedNodeIdsChange ? (selected) => setSelection(item.node.id, selected) : undefined}
      />)}
    </PreviewMosaic>
  </div>;
}

export function NodePlaybackView({
  node,
  report,
  lastContactAt,
  health,
  healthDetail,
  observedNow,
  expectedNow,
  next,
  capture,
  capabilities,
  actions,
  now,
}: NodePlaybackViewProps) {
  const summary = nodeHealth({ report, health, healthDetail });
  const observed = report?.currentContent;
  const observedTitle = observedNow?.title ?? (observed?.mediaId ? "Media playing" : observed?.state === "off_air" ? "Off air" : "Playback state");
  const observedSubtitle = observedNow?.subtitle ?? ([observedNow?.channelName, observedNow?.blockTitle].filter(Boolean).join(" · ") || undefined);
  const expectedHeadingId = React.useId();
  const captureHeadingId = React.useId();

  return <div className="space-y-6">
    <div className="flex flex-col justify-between gap-3 rounded-lg border border-border bg-card p-4 sm:flex-row sm:items-center">
      <StatusSummary status={summary.status} label={summary.label} detail={summary.detail} tone={summary.tone} ariaLive="polite" />
      <LastContact value={lastContactAt} now={now} />
    </div>

    {observed?.state === "playing" ? <NowPlayingCard
      title={observedTitle}
      subtitle={observedSubtitle}
      status={observed.state}
      statusLabel="Playing"
      statusTone="success"
      dimensions={observed.width && observed.height ? { width: observed.width, height: observed.height } : undefined}
      positionMs={observed.positionMs}
      durationMs={observedNow?.durationMs}
      startedAt={observed.startedAt}
      details={<ObservedIdentity report={report} observedNow={observedNow} />}
      actions={actions}
    /> : observed ? <Card>
      <CardHeader className="flex-row items-center justify-between gap-3">
        <CardTitle>{humanizeStatus(observed.state)}</CardTitle>
        <StatusSummary status={observed.state} label={humanizeStatus(observed.state)} tone={toneForPlayback(observed.state)} ariaLive="polite" />
      </CardHeader>
      <CardContent className="space-y-4">
        <ObservedIdentity report={report} observedNow={observedNow} />
        {actions && <div className="flex flex-wrap gap-2 border-t border-border pt-4" aria-label={`${node.name} actions`}>{actions}</div>}
      </CardContent>
    </Card> : <Card>
      <CardHeader><CardTitle>Playback observation unavailable</CardTitle></CardHeader>
      <CardContent>
        <p className="text-sm text-muted-foreground">This node has not reported current content. It may be an older player or have no recent observation; expected content is not treated as observed.</p>
        {actions && <div className="mt-4 flex flex-wrap gap-2" aria-label={`${node.name} actions`}>{actions}</div>}
      </CardContent>
    </Card>}

    <div className="grid gap-4 lg:grid-cols-2">
      <section aria-labelledby={expectedHeadingId} className="space-y-3">
        <div>
          <h2 id={expectedHeadingId} className="text-base font-semibold tracking-tight">Expected now</h2>
          <p className="text-xs text-muted-foreground">Schedule or assignment metadata, not observed pixels.</p>
        </div>
        <ExpectedPreview expected={expectedNow} />
        <MetadataSummary metadata={expectedNow} empty="No expected metadata supplied." />
      </section>
      <section aria-labelledby={captureHeadingId} className="space-y-3">
        <div>
          <h2 id={captureHeadingId} className="text-base font-semibold tracking-tight">Actual capture</h2>
          <p className="text-xs text-muted-foreground">A screenshot supplied by the node, when available.</p>
        </div>
        <CapturePreview nodeName={node.name} capture={capture} />
      </section>
    </div>

    {next && <Card>
      <CardHeader><CardTitle>Up next</CardTitle></CardHeader>
      <CardContent><MetadataSummary metadata={next} empty="No next item supplied." /></CardContent>
    </Card>}

    <NodeTechnicalDetails node={node} report={report} capabilities={capabilities} />
  </div>;
}
