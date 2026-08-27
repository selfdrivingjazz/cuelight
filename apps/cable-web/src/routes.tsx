import * as React from "react";
import { createRootRoute, createRoute, createRouter, Link, Outlet, useNavigate } from "@tanstack/react-router";
import { HttpPresentationSource, loadPresentation, type Channel, type Occurrence, type Presentation, type PresentationSource } from "./presentation";
import { initialRuntimeState, runtimeReducer, type RuntimeAction, type RuntimeState } from "./state";

interface RuntimeContextValue {
  state: RuntimeState;
  dispatch: React.Dispatch<RuntimeAction>;
}

const RuntimeContext = React.createContext<RuntimeContextValue | null>(null);

export function useRuntime(): RuntimeContextValue {
  const context = React.useContext(RuntimeContext);
  if (!context) throw new Error("Runtime context is unavailable");
  return context;
}

export interface RuntimeProviderProps {
  children: React.ReactNode;
  presentation?: Presentation;
  source?: PresentationSource;
}

export function RuntimeProvider({ children, presentation, source }: RuntimeProviderProps) {
  const defaultSource = React.useMemo(() => new HttpPresentationSource(), []);
  const activeSource = source ?? defaultSource;
  const [state, reducerDispatch] = React.useReducer(runtimeReducer, initialRuntimeState);
  const [attempt, setAttempt] = React.useState(0);
  const dispatch = React.useCallback<React.Dispatch<RuntimeAction>>((action) => {
    if (action.type === "RETRY_REVISION") setAttempt((value) => value + 1);
    reducerDispatch(action);
  }, []);

  React.useEffect(() => {
    let cancelled = false;
    reducerDispatch({ type: "START_REVISION_LOAD" });
    const read = loadPresentation(presentation
      ? { read: async () => presentation }
      : activeSource);
    void read.then((value) => {
      if (!cancelled) reducerDispatch({ type: "SYNC_REVISION", presentation: value });
    }).catch((error: unknown) => {
      if (cancelled) return;
      reducerDispatch({
        type: "REVISION_FAILED",
        message: error instanceof Error ? error.message : "The node bridge could not be reached.",
      });
    });
    return () => {
      cancelled = true;
    };
  }, [activeSource, attempt, presentation]);

  const announcedRevisions = React.useRef(new Set<string>());
  React.useEffect(() => {
    if (presentation || !activeSource.markReady) return;
    if (state.kind !== "ready" && state.kind !== "stale-offline" && state.kind !== "empty-lineup") return;
    const revisionKey = `${state.presentation.revision.id}:${state.presentation.revision.digest}`;
    if (announcedRevisions.current.has(revisionKey)) return;
    announcedRevisions.current.add(revisionKey);
    let cancelled = false;
    void activeSource.markReady().catch((error: unknown) => {
      announcedRevisions.current.delete(revisionKey);
      if (!cancelled) {
        reducerDispatch({
          type: "REVISION_FAILED",
          message: error instanceof Error ? error.message : "The node bridge readiness signal could not be sent.",
        });
      }
    });
    return () => {
      cancelled = true;
    };
  }, [activeSource, presentation, state]);

  return <RuntimeContext.Provider value={{ state, dispatch }}>{children}</RuntimeContext.Provider>;
}

function presentationOf(state: RuntimeState): Presentation | undefined {
  return "presentation" in state ? state.presentation : undefined;
}

function formatInstant(value: string, timezone: string, options: Intl.DateTimeFormatOptions = {}) {
  return new Intl.DateTimeFormat(undefined, { timeZone: timezone, dateStyle: "medium", timeStyle: "short", ...options }).format(new Date(value));
}

function channelFor(presentation: Presentation, channelId: string | null): Channel | undefined {
  return presentation.channels.find((channel) => channel.id === channelId);
}

function occurrenceForState(state: RuntimeState): Occurrence | undefined {
  if (state.kind === "warming" || state.kind === "player-opening" || state.kind === "player-ready" || state.kind === "player-failure") return state.occurrence;
  if (state.kind === "ready" || state.kind === "stale-offline") {
    return state.presentation.occurrences.find((occurrence) => occurrence.channelId === state.selectedId) ?? state.presentation.occurrences[0];
  }
  return undefined;
}

function Shell() {
  const { state, dispatch } = useRuntime();
  const navigate = useNavigate();
  const presentation = presentationOf(state);
  const selectedChannel = presentation && "selectedId" in state ? channelFor(presentation, state.selectedId) : undefined;
  const activeOccurrence = occurrenceForState(state);
  const headerChannel = selectedChannel ?? (presentation && activeOccurrence ? channelFor(presentation, activeOccurrence.channelId) : undefined);
  const openDiagnostics = () => {
    dispatch({ type: "OPEN_DIAGNOSTICS" });
    void navigate({ to: "/diagnostics" });
  };
  return (
    <div className={`cable-shell theme-${presentation?.theme ?? "nebula"}`} data-runtime-state={state.kind}>
      <header className="cable-header">
        <Link className="wordmark" to="/" aria-label="Cable guide">CABLE<span>·</span></Link>
        <div className="header-selection" aria-live="polite">
          <span className="header-selection-label">TUNED</span>
          <strong>{headerChannel ? `${String(headerChannel.number).padStart(2, "0")} / ${headerChannel.callsign}` : "SCANNING"}</strong>
        </div>
        <nav aria-label="Runtime navigation">
          <Link to="/">Guide</Link>
          <Link to="/placard">Placard</Link>
          <Link to="/diagnostics">Diagnostics</Link>
        </nav>
        <div className="signal-state" data-state={presentation?.bridge.state ?? "syncing"}>
          <span aria-hidden="true" />
          <span>{presentation?.bridge.state ?? "syncing"}</span>
        </div>
      </header>
      <main className="cable-main"><Outlet /></main>
      <footer className="cable-footer">
        <span>REV {presentation?.revision.id ?? "—"}</span>
        <span>{presentation ? `GENERATED ${formatInstant(presentation.revision.generatedAt, presentation.revision.timezone)}` : "WAITING FOR REVISION"}</span>
        <button type="button" onClick={openDiagnostics}>Signal details <span aria-hidden="true">↗</span></button>
      </footer>
    </div>
  );
}

function SyncState() {
  return (
    <section className="state-panel state-panel-sync" aria-live="polite">
      <p className="eyebrow">SIGNAL SYNC</p>
      <h1>Finding your lineup</h1>
      <p>The player is checking the assigned revision and bridge.</p>
      <div className="meter" role="progressbar" aria-label="Loading presentation" aria-valuetext="Synchronizing"><span /></div>
      <p className="state-caption">No schedule is displayed until the presentation timestamp is verified.</p>
    </section>
  );
}

function ErrorState({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <section className="state-panel state-panel-error" aria-live="assertive">
      <p className="eyebrow">SIGNAL ERROR</p>
      <h1>Unable to load lineup</h1>
      <p>{message}</p>
      <div className="state-actions"><button className="button" type="button" onClick={onRetry}>Retry bridge</button></div>
    </section>
  );
}

function EmptyState({ presentation }: { presentation: Presentation }) {
  return (
    <section className="state-panel state-panel-empty" aria-live="polite">
      <p className="eyebrow">NO PROGRAMS</p>
      <h1>Nothing scheduled</h1>
      <p>The assigned revision contains no playable occurrences. Cable is standing by for the next verified lineup.</p>
      <div className="state-stamp">{presentation.revision.id} · {presentation.revision.timezone}</div>
      <Link className="button" to="/diagnostics">Open diagnostics</Link>
    </section>
  );
}

function GuidePage() {
  const { state, dispatch } = useRuntime();
  const navigate = useNavigate();
  if (state.kind === "boot" || state.kind === "revision-loading") return <SyncState />;
  if (state.kind === "error") return <ErrorState message={state.message} onRetry={() => dispatch({ type: "RETRY_REVISION" })} />;
  if (state.kind === "empty-lineup") return <EmptyState presentation={state.presentation} />;
  if (state.kind === "diagnostics") return <DiagnosticsPage />;
  if (!("presentation" in state)) return <SyncState />;
  const presentation = state.presentation;
  const enabledChannels = presentation.channels.filter((channel) => channel.enabled);
  const activeChannelId = (state.kind === "ready" || state.kind === "stale-offline") ? state.selectedId ?? enabledChannels[0]?.id : enabledChannels[0]?.id;
  const activeChannelIndex = Math.max(0, enabledChannels.findIndex((channel) => channel.id === activeChannelId));
  const isOffline = state.kind === "stale-offline";
  const selectChannel = (channel: Channel) => dispatch({ type: "SELECT_CHANNEL", channelId: channel.id });
  const tune = (occurrence: Occurrence) => {
    dispatch({ type: "SELECT_PROGRAM", occurrence });
    void navigate({ to: "/player" });
  };
  const moveChannel = (delta: number) => {
    const channel = enabledChannels[(activeChannelIndex + delta + enabledChannels.length) % enabledChannels.length];
    if (channel) selectChannel(channel);
  };
  const activeOccurrence = presentation.occurrences.find((occurrence) => occurrence.channelId === activeChannelId) ?? presentation.occurrences[0];

  return (
    <section className="guide-page" aria-label="Cable programme guide">
      <div className="guide-intro">
        <div>
          <p className="eyebrow">CABLE / TONIGHT</p>
          <h1>Tonight on Cable</h1>
          <p className="guide-deck">A precise window into the assigned presentation. Tune a channel, then open its verified artifact.</p>
        </div>
        <div className="guide-revision" aria-label="Presentation revision">
          <span>REVISION</span>
          <strong>{presentation.revision.id}</strong>
          <small>{formatInstant(presentation.revision.generatedAt, presentation.revision.timezone)}</small>
        </div>
      </div>
      {isOffline && <div className="state-banner state-banner-warning" role="status"><span className="status-dot" aria-hidden="true" />Offline presentation · showing the last verified revision</div>}
      <div className="channel-rail" aria-label="Tune channel">
        {enabledChannels.map((channel) => <button key={channel.id} className={`channel-chip ${channel.id === activeChannelId ? "is-selected" : ""}`} type="button" aria-pressed={channel.id === activeChannelId} onClick={() => selectChannel(channel)}><b>{String(channel.number).padStart(2, "0")}</b><span>{channel.callsign}</span></button>)}
      </div>
      <div className="guide-key-hint"><span>CHANNEL SCAN</span><kbd>↑</kbd><kbd>↓</kbd><span>to tune</span><span className="guide-key-hint-touch">Touch a channel or programme to tune</span></div>
      <div className="guide-grid" tabIndex={0} aria-label="Programme grid" onKeyDown={(event) => { if (event.key === "ArrowUp" || event.key === "ArrowLeft") { event.preventDefault(); moveChannel(-1); } if (event.key === "ArrowDown" || event.key === "ArrowRight") { event.preventDefault(); moveChannel(1); } }}>
        {presentation.channels.map((channel) => {
          const occurrences = presentation.occurrences.filter((occurrence) => occurrence.channelId === channel.id);
          const selected = channel.id === activeChannelId;
          return (
            <article key={channel.id} className={`channel-row ${selected ? "is-selected" : ""} ${!channel.enabled ? "is-disabled" : ""}`} style={{ "--accent": channel.accent } as React.CSSProperties}>
              <button className="channel-label" type="button" disabled={!channel.enabled} aria-label={`Tune ${channel.number} ${channel.callsign}`} aria-pressed={selected} onClick={() => selectChannel(channel)}>
                <b>{String(channel.number).padStart(2, "0")}</b><span>{channel.callsign}</span><small>{channel.name}</small><i aria-hidden="true">{selected ? "●" : "○"}</i>
              </button>
              <div className="programs" aria-label={`${channel.callsign} programmes`}>
                {occurrences.length ? occurrences.map((occurrence) => <button key={occurrence.id} className={`program-card ${activeOccurrence?.id === occurrence.id ? "is-selected" : ""}`} type="button" onClick={() => tune(occurrence)}>
                  <span className="program-time">{formatInstant(occurrence.startsAt, presentation.revision.timezone, { dateStyle: undefined, timeStyle: "short" })} — {formatInstant(occurrence.endsAt, presentation.revision.timezone, { dateStyle: undefined, timeStyle: "short" })}</span>
                  <strong>{occurrence.title}</strong>
                  <span>{occurrence.subtitle ?? "Programme"}</span>
                  <small>{occurrence.media.kind.toUpperCase()} · {occurrence.media.mime}</small>
                  <em>OPEN PROGRAMME <span aria-hidden="true">↗</span></em>
                </button>) : <div className="channel-standby"><span aria-hidden="true">∿</span>{channel.standbyMessage ?? "No verified occurrence"}</div>}
              </div>
            </article>
          );
        })}
      </div>
      {activeOccurrence && <aside className="guide-placard" aria-label="Selected programme placard">
        <div className="placard-marker"><span aria-hidden="true" />NOW SELECTED</div>
        <div className="guide-placard-copy"><strong>{activeOccurrence.title}</strong><span>{activeOccurrence.subtitle ?? "Ready to tune"}</span></div>
        <Link className="placard-link" to="/placard" onClick={() => dispatch({ type: "OPEN_PLACARD" })}>Open placard <span aria-hidden="true">↗</span></Link>
      </aside>}
    </section>
  );
}

function PlayerPage() {
  const { state, dispatch } = useRuntime();
  const navigate = useNavigate();
  React.useEffect(() => {
    if (state.kind !== "warming") return;
    const timer = window.setTimeout(() => dispatch({ type: "ARTIFACT_READY" }), 320);
    return () => window.clearTimeout(timer);
  }, [dispatch, state.kind]);
  if (state.kind === "boot" || state.kind === "revision-loading") return <SyncState />;
  if (state.kind === "error") return <ErrorState message={state.message} onRetry={() => dispatch({ type: "RETRY_REVISION" })} />;
  if (state.kind === "warming") return <PlayerState title="Warming artifact" detail="Preparing the exact verified media reference." phase="WARMING ARTIFACT" />;
  if (state.kind === "player-opening") return <PlayerSurface occurrence={state.occurrence} presentation={state.presentation} opening onReady={() => dispatch({ type: "PLAYER_READY" })} onFailure={() => dispatch({ type: "PLAYER_FAILED", message: "The verified media artifact could not be opened." })} onBack={() => { dispatch({ type: "BACK_TO_GUIDE" }); void navigate({ to: "/" }); }} />;
  if (state.kind === "player-failure") return <PlayerState title="Playback unavailable" detail={state.message} phase="PLAYER FAILURE"><button className="button" type="button" onClick={() => dispatch({ type: "RETRY_PLAYER" })}>Retry playback</button><button className="button secondary" type="button" onClick={() => void navigate({ to: "/diagnostics" })}>Open diagnostics</button></PlayerState>;
  if (state.kind !== "player-ready") return <PlayerState title="No player target" detail="Choose a programme from the guide to begin tuning." phase="PLAYER STANDBY"><Link className="button" to="/">Return to guide</Link></PlayerState>;
  return <PlayerSurface occurrence={state.occurrence} presentation={state.presentation} onFailure={() => dispatch({ type: "PLAYER_FAILED", message: "The verified media artifact stopped unexpectedly." })} onBack={() => { dispatch({ type: "BACK_TO_GUIDE" }); void navigate({ to: "/" }); }} />;
}

function PlayerState({ title, detail, phase, children }: { title: string; detail: string; phase: string; children?: React.ReactNode }) {
  return <section className="player-surface player-state" aria-live="polite"><div className="player-state-grid" aria-hidden="true" /><div className="player-state-content"><p className="eyebrow">{phase}</p><h1>{title}</h1><p>{detail}</p><div className="meter"><span /></div><div className="state-actions">{children}</div></div></section>;
}

function PlayerSurface({ occurrence, presentation, opening = false, onReady, onFailure, onBack }: { occurrence: Occurrence; presentation: Presentation; opening?: boolean; onReady?: () => void; onFailure: () => void; onBack: () => void }) {
  const [volume, setVolume] = React.useState(() => Math.round(occurrence.volume ?? 100));
  const [muted, setMuted] = React.useState(occurrence.muted ?? false);
  const video = React.useRef<HTMLVideoElement>(null);
  const channel = presentation.channels.find((candidate) => candidate.id === occurrence.channelId);
  const volumeValue = muted ? 0 : volume;
  React.useEffect(() => {
    if (!video.current) return;
    video.current.volume = volume / 100;
    video.current.muted = muted;
  }, [muted, volume]);
  const media = occurrence.media.kind === "video"
    ? <video ref={video} className="player-content-media" src={occurrence.media.reference} autoPlay playsInline loop={occurrence.loop ?? false} muted={muted} onCanPlay={onReady} onError={onFailure} />
    : <img className="player-content-media" src={occurrence.media.reference} alt={occurrence.title} onLoad={onReady} onError={onFailure} />;
  return <section className="player-surface" aria-label="Cable player">
    <div className="player-toolbar"><button className="text-button" type="button" onClick={onBack}><span aria-hidden="true">←</span> Guide</button><div className="player-toolbar-state"><span className="status-dot" aria-hidden="true" />{opening ? "TUNING / VERIFIED" : "LIVE / VERIFIED"}</div><span>{channel ? `${String(channel.number).padStart(2, "0")} ${channel.callsign}` : "CABLE"}</span></div>
    <div className="player-viewport" data-media-kind={occurrence.media.kind} data-fit={occurrence.fit ?? "fit"}>
      {media}
      <div className="player-corner player-corner-top">{occurrence.media.kind.toUpperCase()} / {occurrence.media.mime}</div>
      <div className="player-title"><p className="eyebrow">{formatInstant(occurrence.startsAt, presentation.revision.timezone, { dateStyle: "medium", timeStyle: undefined })}</p><h1>{occurrence.title}</h1><p>{occurrence.subtitle ?? "Cable presentation"}</p><small>REFERENCE · {occurrence.media.reference}</small></div>
      <div className="player-corner player-corner-bottom">{presentation.revision.digest}</div>
      {opening && <div className="player-opening-overlay" role="status">Tuning signal</div>}
    </div>
    <div className="player-controls">
      <div className="player-control-copy"><strong>{occurrence.title}</strong><span>{formatInstant(occurrence.startsAt, presentation.revision.timezone, { dateStyle: undefined, timeStyle: "short" })} — {formatInstant(occurrence.endsAt, presentation.revision.timezone, { dateStyle: undefined, timeStyle: "short" })}</span></div>
      <div className="volume-control"><button className="icon-button" type="button" aria-label={muted ? "Unmute" : "Mute"} onClick={() => setMuted((value) => !value)}>{muted ? "×" : "⌁"}</button><label htmlFor="cable-volume">Volume <output htmlFor="cable-volume">{volumeValue}</output></label><input id="cable-volume" type="range" min="0" max="100" value={volumeValue} onChange={(event) => { setVolume(Number(event.currentTarget.value)); setMuted(false); }} /></div>
    </div>
  </section>;
}

function PlacardPage() {
  const { state, dispatch } = useRuntime();
  const navigate = useNavigate();
  if (state.kind === "error") return <ErrorState message={state.message} onRetry={() => dispatch({ type: "RETRY_REVISION" })} />;
  if (!("presentation" in state)) return <SyncState />;
  const occurrence = occurrenceForState(state);
  const isOffline = state.kind === "stale-offline" || state.presentation.revision.stale;
  const tune = () => {
    if (!occurrence) return;
    dispatch({ type: "SELECT_PROGRAM", occurrence });
    void navigate({ to: "/player" });
  };
  return <section className="placard-page"><div className="placard-orbit" aria-hidden="true" /><div className="placard-kicker"><span className="status-dot" aria-hidden="true" />CABLE / PRESENTATION PLACARD</div>{isOffline && <div className="state-banner state-banner-warning" role="status">Stale revision · timestamps below are authoritative archive times</div>}{occurrence ? <><p className="placard-channel">{state.presentation.channels.find((channel) => channel.id === occurrence.channelId)?.callsign ?? "CABLE"}</p><h1>{occurrence.title}</h1><p className="placard-subtitle">{occurrence.subtitle ?? "Now showing"}</p><div className="placard-meta"><span>{formatInstant(occurrence.startsAt, state.presentation.revision.timezone)}</span><span>REV {state.presentation.revision.id}</span><span>{state.presentation.revision.timezone}</span></div><p className="placard-reference">{occurrence.media.kind.toUpperCase()} · {occurrence.media.reference}</p></> : <><h1>Standby</h1><p className="placard-subtitle">No presentation is currently available.</p></>}<div className="state-actions"><button className="button" type="button" disabled={!occurrence} onClick={tune}>Tune programme</button><button className="button secondary" type="button" onClick={() => { dispatch({ type: "BACK_TO_GUIDE" }); void navigate({ to: "/" }); }}>Return to guide</button></div></section>;
}
function DiagnosticsPage() {
  const { state, dispatch } = useRuntime();
  const navigate = useNavigate();
  if (state.kind === "error") return <ErrorState message={state.message} onRetry={() => dispatch({ type: "RETRY_REVISION" })} />;
  if (!("presentation" in state)) return <SyncState />;
  const { presentation } = state;
  const returnToGuide = () => {
    dispatch({ type: "BACK_TO_GUIDE" });
    void navigate({ to: "/" });
  };
  return <section className="diagnostics"><p className="eyebrow">DIAGNOSTICS / READ-ONLY</p><h1>Signal details</h1><p className="diagnostics-deck">The bridge exposes this presentation without changing its schedule or timestamps.</p><dl><div><dt>Runtime</dt><dd>{state.kind}</dd></div><div><dt>Revision</dt><dd>{presentation.revision.id}</dd></div><div><dt>Digest</dt><dd>{presentation.revision.digest}</dd></div><div><dt>Generated</dt><dd>{formatInstant(presentation.revision.generatedAt, presentation.revision.timezone)}</dd></div><div><dt>Bridge</dt><dd>{presentation.bridge.state}</dd></div><div><dt>Protocol</dt><dd>{presentation.bridge.protocolVersion}</dd></div><div><dt>Occurrences</dt><dd>{presentation.occurrences.length}</dd></div></dl><button className="button" type="button" onClick={returnToGuide}>Return to guide</button></section>;
}

const rootRoute = createRootRoute({ component: Shell });
const guideRoute = createRoute({ getParentRoute: () => rootRoute, path: "/", component: GuidePage });
const playerRoute = createRoute({ getParentRoute: () => rootRoute, path: "/player", component: PlayerPage });
const placardRoute = createRoute({ getParentRoute: () => rootRoute, path: "/placard", component: PlacardPage });
const diagnosticsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/diagnostics", component: DiagnosticsPage });
const routeTree = rootRoute.addChildren([guideRoute, playerRoute, placardRoute, diagnosticsRoute]);
export const router = createRouter({ routeTree });
declare module "@tanstack/react-router" { interface Register { router: typeof router } }
