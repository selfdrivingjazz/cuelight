import * as React from "react";
import type { components } from "@cable/api-client";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  NowPlayingCard,
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
  /** An already-authorized object URL for a still: the media's persisted poster, or a web page capture when `media` is absent. */
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
  /** Provenance reported by the screenshot job, never inferred from playback metadata. */
  provenance?: string;
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

/** A screen whose last report is older than this is treated as offline rather than showing its stale state. */
const offlineAfterMs = 10 * 60_000;

export function isScreenOffline(report: NodeReport | undefined, now: number = Date.now()): boolean {
  const reportedAt = report?.reportedAt ? Date.parse(report.reportedAt) : Number.NaN;
  return Number.isFinite(reportedAt) && now - reportedAt > offlineAfterMs;
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

  if (isScreenOffline(report)) return { status: "offline", label: "Offline", detail: "No recent report", tone: "danger" };

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

/** `mediaPreviewUrl` is always a still: the media's persisted poster or a web capture. */
function mediaPreview(metadata: PlaybackMetadata | undefined): PreviewMedia | undefined {
  if (!metadata?.mediaPreviewUrl) return undefined;
  return { kind: "image", src: metadata.mediaPreviewUrl, alt: `Preview of ${metadata.title}` };
}

function CapturePreview({ nodeName, capture, compact = false }: { nodeName: string; capture?: NodeCapture; compact?: boolean }) {
  return <div>{!capture && <p className="sr-only">No capture exists</p>}<PreviewSurface
    aria-label={`Actual capture for ${nodeName}`}
    media={capture ? { kind: "image", src: capture.url, alt: capture.alt ?? `Actual capture from ${nodeName}` } : undefined}
    state={capture ? undefined : "empty"}
    className={compact ? "rounded-none border-x-0" : undefined}
    emptyTitle="No capture exists"
    emptyDescription="The node has not supplied a screenshot. No image is inferred from its playback report."
  />{capture && <p className="px-4 py-2 text-xs text-muted-foreground"><span className="font-medium text-foreground">Actual node capture</span>{capture.capturedAt && <> · <time dateTime={capture.capturedAt}>Captured {formatDateTime(capture.capturedAt) ?? "at an unknown time"}</time></>}{capture.provenance && <> · {capture.provenance}</>}</p>}</div>;
}

function ExpectedPreview({ expected, compact = false }: { expected?: PlaybackMetadata; compact?: boolean }) {
  const preview = mediaPreview(expected);
  if (compact && !preview) return <p className="px-4 py-6 text-sm text-muted-foreground">{expected ? "No preview available for this assignment." : "No content assigned."}</p>;
  return <PreviewSurface
    aria-label="Expected media preview"
    media={preview}
    state={preview ? undefined : "empty"}
    className={compact ? "rounded-none border-x-0" : undefined}
    fit={expected?.media?.kind === "web" || expected?.media?.playback?.fit === "fill" ? "cover" : "contain"}
    emptyTitle={expected ? "Expected preview unavailable" : "No expected content"}
    emptyDescription={expected ? "Expected metadata is available, but no media preview URL was supplied." : "No current schedule or assignment metadata was supplied."}
  />;
}
function ObservedPreview({ observed, compact = false }: { observed?: PlaybackMetadata; compact?: boolean }) {
  const preview = mediaPreview(observed);
  if (compact && !preview) return <p className="px-4 py-6 text-sm text-muted-foreground">{observed ? "No preview available for the reported media." : "No media reported."}</p>;
  return <PreviewSurface
    aria-label="Reported media preview"
    media={preview}
    state={preview ? undefined : "empty"}
    className={compact ? "rounded-none border-x-0" : undefined}
    fit={observed?.media?.kind === "web" || observed?.media?.playback?.fit === "fill" ? "cover" : "contain"}
    emptyTitle={observed ? "Reported media preview unavailable" : "No reported media"}
    emptyDescription={observed ? "The player reported this media identity, but no preview URL was supplied." : "The node has not reported a current media identity."}
    overlay={preview ? <span>Reported identity · not a screenshot</span> : undefined}
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

function screenSource({ capture, expectedNow, observedNow }: Pick<FleetNodeVisual, "capture" | "expectedNow" | "observedNow">): { label: string; media?: PreviewMedia; fit: "contain" | "cover" } {
  if (capture) return { label: "Capture", media: { kind: "image", src: capture.url, alt: capture.alt ?? "Actual node capture" }, fit: "cover" };
  const expected = mediaPreview(expectedNow);
  if (expected) return { label: "Expected", media: expected.kind === "video" ? { ...expected, controls: false, muted: true, autoPlay: true, loop: true } : expected, fit: expectedNow?.media?.kind === "web" || expectedNow?.media?.playback?.fit === "fill" ? "cover" : "contain" };
  const observed = mediaPreview(observedNow);
  if (observed) return { label: "Reported", media: observed.kind === "video" ? { ...observed, controls: false, muted: true, autoPlay: true, loop: true } : observed, fit: "contain" };
  return { label: expectedNow ? "Expected" : "No preview", fit: "contain" };
}

/** Compact wall tile: a monitor-framed preview, the screen name, what it shows, and a tally status. */
export function ScreenTile({ node, report, lastContactAt, health, healthDetail, observedNow, expectedNow, capture, nodeHref, selected = false, onSelectedChange, now }: Omit<FleetNodeCardProps, "actions" | "capabilities" | "next">) {
  const summary = nodeHealth({ report, health, healthDetail });
  const source = screenSource({ capture, expectedNow, observedNow });
  const showing = observedNow?.title ?? expectedNow?.title ?? (report?.currentContent?.state === "off_air" ? "Off air" : report?.currentContent ? humanizeStatus(report.currentContent.state) : "Nothing reported");
    return <article aria-label={`Screen ${node.name}`} data-selected={selected || undefined} className={`group relative min-w-0 rounded-lg border bg-card p-2 transition-colors hover:border-input ${selected ? "border-primary ring-1 ring-primary" : "border-border"}`}>
    <div className="monitor relative">
      {source.media
        ? <PreviewSurface media={source.media} fit={source.fit} aria-label={`${source.label} preview for ${node.name}`} />
        : <div className="no-signal relative grid aspect-video place-items-center overflow-hidden rounded-[5px]" aria-label={`No preview for ${node.name}`} role="img"><span className="relative z-10 rounded-sm bg-black/70 px-2 py-1 font-mono text-[11px] uppercase tracking-[0.16em] text-white/85">{expectedNow ? expectedNow.title : "No signal"}</span></div>}
      <span className="pointer-events-none absolute left-3 top-3 z-10 rounded-sm bg-black/65 px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-[0.14em] text-white/90">{source.label}</span>
      {node.nodeClass === "browser" && <span className="pointer-events-none absolute right-3 top-3 z-10 rounded-sm bg-black/65 px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-[0.14em] text-white/90">Browser</span>}
    </div>
    <div className="flex items-start justify-between gap-3 px-1.5 pt-2.5">
      <div className="min-w-0">
        <h2 className="truncate font-semibold tracking-tight">{nodeHref ? <a className="after:absolute after:inset-0 after:rounded-lg focus-visible:shadow-none focus-visible:after:shadow-[var(--focus-ring)]" href={nodeHref}>{node.name}</a> : node.name}</h2>
        <p className="mt-0.5 truncate text-sm text-muted-foreground">{showing}</p>
      </div>
      {onSelectedChange && <label className="relative z-10 -m-1 inline-flex cursor-pointer p-1"><input type="checkbox" className="h-4 w-4 rounded border-input accent-primary" checked={selected} onChange={(event) => onSelectedChange(event.currentTarget.checked)} /><span className="sr-only">Select {node.name}</span></label>}
    </div>
    <div className="flex items-center justify-between gap-3 px-1.5 pb-1 pt-2">
      <StatusSummary className="text-xs" status={summary.status} label={summary.label} tone={summary.tone} ariaLive="polite" />
      <span className="shrink-0 font-mono text-[11px] text-muted-foreground">{lastContactAt ? formatLastContact(lastContactAt, now) : "—"}</span>
    </div>
  </article>;
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

  const statusRow = <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border pt-3">
    <StatusSummary status={summary.status} label={summary.label} detail={summary.detail} tone={summary.tone} ariaLive="polite" />
    <LastContact value={lastContactAt} now={now} />
  </div>;

  return <div className="space-y-5">
    <div className="grid gap-5 lg:grid-cols-2">
      <section aria-labelledby={expectedHeadingId} className="min-w-0 space-y-2.5">
        <div className="flex items-baseline justify-between gap-3">
          <h2 id={expectedHeadingId} className="eyebrow text-foreground/80">{expectedNow ? "Expected now" : observedNow ? "Reported media" : "Expected now"}</h2>
          <p className="truncate text-xs text-muted-foreground">{expectedNow ? "From the assignment — not observed pixels" : observedNow ? "Identity reported by the player — not a screenshot" : "No expected schedule or assignment metadata is available."}</p>
        </div>
        <div className="monitor">{expectedNow ? <ExpectedPreview expected={expectedNow} /> : <ObservedPreview observed={observedNow} />}</div>
        <MetadataSummary metadata={expectedNow ?? observedNow} empty={expectedNow ? "No expected metadata supplied." : "No reported media metadata supplied."} />
      </section>
      <section aria-labelledby={captureHeadingId} className="min-w-0 space-y-2.5">
        <div className="flex items-baseline justify-between gap-3">
          <h2 id={captureHeadingId} className="eyebrow text-foreground/80">Actual capture</h2>
          <p className="truncate text-xs text-muted-foreground">A screenshot taken by the screen itself</p>
        </div>
        <div className="monitor"><CapturePreview nodeName={node.name} capture={capture} /></div>
        {actions && <div className="flex flex-wrap items-center gap-2" aria-label={`${node.name} actions`}>{actions}</div>}
      </section>
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
      details={<><ObservedIdentity report={report} observedNow={observedNow} />{statusRow}</>}
    /> : observed ? <Card>
      <CardHeader className="flex-row items-center justify-between gap-3">
        <CardTitle>{humanizeStatus(observed.state)}</CardTitle>
        <StatusSummary status={observed.state} label={humanizeStatus(observed.state)} tone={toneForPlayback(observed.state)} ariaLive="polite" />
      </CardHeader>
      <CardContent className="space-y-4">
        <ObservedIdentity report={report} observedNow={observedNow} />
        {statusRow}
      </CardContent>
    </Card> : <Card>
      <CardHeader><CardTitle>Playback observation unavailable</CardTitle></CardHeader>
      <CardContent className="space-y-4">
        <p className="text-sm text-muted-foreground">This node has not reported current content. It may be an older player or have no recent observation; expected content is not treated as observed.</p>
        {statusRow}
      </CardContent>
    </Card>}

    {next && <Card>
      <CardHeader><CardTitle>Up next</CardTitle></CardHeader>
      <CardContent><MetadataSummary metadata={next} empty="No next item supplied." /></CardContent>
    </Card>}

    <NodeTechnicalDetails node={node} report={report} capabilities={capabilities} />
  </div>;
}
