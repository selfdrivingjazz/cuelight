import * as React from "react";
import { createRootRoute, createRoute, createRouter, Link, Outlet, useNavigate } from "@tanstack/react-router";
import { HttpPresentationSource, loadPresentation, type Occurrence, type Presentation, type PresentationSource } from "./presentation";
import { initialRuntimeState, runtimeReducer, type RuntimeAction, type RuntimeState } from "./state";
import { LegacyGuide } from "./components/LegacyGuide";
import { SplashScreen } from "./components/SplashScreen";
import { useRemoteInput, type RemoteEvent } from "./components/RemoteInput";

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


function occurrenceForState(state: RuntimeState): Occurrence | undefined {
  if (state.kind === "warming" || state.kind === "player-opening" || state.kind === "player-ready" || state.kind === "player-failure") return state.occurrence;
  if (state.kind === "ready" || state.kind === "stale-offline") {
    return state.presentation.occurrences.find((occurrence) => occurrence.channelId === state.selectedId) ?? state.presentation.occurrences[0];
  }
  return undefined;
}

function Shell() {
  const { state } = useRuntime();
  const presentation = presentationOf(state);
  const [showSplash, setShowSplash] = React.useState(true);
  React.useEffect(() => {
    const timer = window.setTimeout(() => setShowSplash(false), 4_700);
    return () => window.clearTimeout(timer);
  }, []);
  return <div className={`legacy-root theme-${presentation?.theme ?? "nebula"}`} data-runtime-state={state.kind}><Outlet /><SplashScreen active={showSplash} /></div>;
}

function SyncState() {
  return (
    <section className="state-panel state-panel-sync" aria-live="polite">
      <h1>Finding your lineup</h1>
      <p>One moment while Cable tunes in.</p>
      <div className="meter" role="progressbar" aria-label="Loading lineup"><span /></div>
    </section>
  );
}

function ErrorState({ onRetry }: { onRetry: () => void }) {
  return (
    <section className="state-panel state-panel-error" aria-live="assertive">
      <h1>Unable to load lineup</h1>
      <p>Check the connection and try again.</p>
      <div className="state-actions"><button className="button" type="button" onClick={onRetry}>Try again</button></div>
    </section>
  );
}

function EmptyState() {
  return (
    <section className="state-panel state-panel-empty" aria-live="polite">
      <h1>Nothing scheduled</h1>
      <p>There are no programmes in the guide right now.</p>
    </section>
  );
}

function GuidePage() {
  const { state, dispatch } = useRuntime();
  const navigate = useNavigate();
  const presentation = "presentation" in state ? state.presentation : undefined;
  const selectedId = state.kind === "ready" || state.kind === "stale-offline" ? state.selectedId : null;
  const tuneChannel = React.useCallback((channelId: string) => {
    if (!presentation) return;
    dispatch({ type: "SELECT_CHANNEL", channelId });
    const now = Date.now();
    const current = presentation.occurrences.find((occurrence) => occurrence.channelId === channelId && new Date(occurrence.startsAt).getTime() <= now && now < new Date(occurrence.endsAt).getTime());
    if (!current) return;
    dispatch({ type: "TUNE_LIVE", occurrence: current });
    void navigate({ to: "/player" });
  }, [dispatch, navigate, presentation]);
  const remoteEvent = React.useCallback((event: RemoteEvent) => {
    if (!presentation || event.type !== "key") return;
    const channels = presentation.channels.filter((channel) => channel.enabled);
    if (channels.length === 0) return;
    const index = Math.max(0, channels.findIndex((channel) => channel.id === selectedId));
    if (event.key === "ArrowUp" || event.key === "ArrowLeft") {
      const channel = channels[(index - 1 + channels.length) % channels.length];
      if (channel) tuneChannel(channel.id);
    } else if (event.key === "ArrowDown" || event.key === "ArrowRight") {
      const channel = channels[(index + 1) % channels.length];
      if (channel) tuneChannel(channel.id);
    } else if (event.key === "Enter") {
      const channel = channels[index];
      if (channel) tuneChannel(channel.id);
    }
  }, [presentation, selectedId, tuneChannel]);
  useRemoteInput(Boolean(presentation?.remote), remoteEvent);
  if (state.kind === "boot" || state.kind === "revision-loading") return <SyncState />;
  if (state.kind === "error") return <ErrorState onRetry={() => dispatch({ type: "RETRY_REVISION" })} />;
  if (state.kind === "empty-lineup") return <EmptyState />;
  if (state.kind === "diagnostics") return <DiagnosticsPage />;
  if (!presentation) return <SyncState />;
  return <LegacyGuide
    presentation={presentation}
    selectedChannelId={selectedId}
    offline={state.kind === "stale-offline" || presentation.revision.stale}
    remoteURL={presentation.remote?.url}
    showQR={presentation.remote?.showQr}
    onSelectChannel={(channel) => tuneChannel(channel.id)}
    onTune={(occurrence) => {
      dispatch({ type: "TUNE_LIVE", occurrence });
      void navigate({ to: "/player" });
    }}
  />;
}

function PlayerPage() {
  const { state, dispatch } = useRuntime();
  const navigate = useNavigate();
  React.useEffect(() => {
    if (state.kind !== "warming") return;
    const timer = window.setTimeout(() => dispatch({ type: "ARTIFACT_READY" }), 320);
    return () => window.clearTimeout(timer);
  }, [dispatch, state.kind]);
  React.useEffect(() => {
    if (state.kind !== "player-opening" && state.kind !== "player-ready") return;
    const endsAt = new Date(state.occurrence.endsAt).getTime();
    const advance = () => {
      const at = Date.now() + 50;
      const next = state.presentation.occurrences.find((occurrence) => occurrence.channelId === state.occurrence.channelId && occurrence.id !== state.occurrence.id && new Date(occurrence.startsAt).getTime() <= at && at < new Date(occurrence.endsAt).getTime());
      if (next) {
        dispatch({ type: "TUNE_LIVE", occurrence: next });
        return;
      }
      dispatch({ type: "BACK_TO_GUIDE" });
      void navigate({ to: "/" });
    };
    if (endsAt <= Date.now()) {
      advance();
      return;
    }
    const timer = window.setTimeout(advance, endsAt - Date.now());
    return () => window.clearTimeout(timer);
  }, [dispatch, navigate, state]);
  if (state.kind === "boot" || state.kind === "revision-loading") return <SyncState />;
  if (state.kind === "error") return <ErrorState onRetry={() => dispatch({ type: "RETRY_REVISION" })} />;
  if (state.kind === "warming") return <PlayerState title="Tuning channel" detail="Please wait while the programme begins." busy />;
  if (state.kind === "player-opening") return <PlayerSurface occurrence={state.occurrence} presentation={state.presentation} opening onReady={() => dispatch({ type: "PLAYER_READY" })} onFailure={() => dispatch({ type: "PLAYER_FAILED", message: "The programme could not be played." })} onBack={() => { dispatch({ type: "BACK_TO_GUIDE" }); void navigate({ to: "/" }); }} />;
  if (state.kind === "player-failure") return <PlayerState title="Playback unavailable" detail={state.message}><button className="button" type="button" onClick={() => dispatch({ type: "RETRY_PLAYER" })}>Try again</button><button className="button secondary" type="button" onClick={() => { dispatch({ type: "BACK_TO_GUIDE" }); void navigate({ to: "/" }); }}>Guide</button></PlayerState>;
  if (state.kind !== "player-ready") return <PlayerState title="No programme selected" detail="Return to the guide to choose a channel."><Link className="button" to="/">Guide</Link></PlayerState>;
  return <PlayerSurface occurrence={state.occurrence} presentation={state.presentation} onFailure={() => dispatch({ type: "PLAYER_FAILED", message: "The programme stopped unexpectedly." })} onBack={() => { dispatch({ type: "BACK_TO_GUIDE" }); void navigate({ to: "/" }); }} />;
}

function PlayerState({ title, detail, busy = false, children }: { title: string; detail: string; busy?: boolean; children?: React.ReactNode }) {
  return <section className="player-surface player-state" aria-live="polite"><div className="player-state-content"><h1>{title}</h1><p>{detail}</p>{busy && <div className="meter" role="progressbar" aria-label="Tuning channel"><span /></div>}<div className="state-actions">{children}</div></div></section>;
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
  const handleRemote = React.useCallback((event: RemoteEvent) => {
    if (event.type !== "key") return;
    if (event.key === "Escape" || event.key === "Backspace") onBack();
    else if (event.key === "VolumeUp") { setMuted(false); setVolume((value) => Math.min(100, value + 5)); }
    else if (event.key === "VolumeDown") { setMuted(false); setVolume((value) => Math.max(0, value - 5)); }
    else if (event.key === "Mute") setMuted((value) => !value);
    else if (event.key === "Enter" && video.current) {
      if (video.current.paused) void video.current.play();
      else video.current.pause();
    }
  }, [onBack]);
  useRemoteInput(Boolean(presentation.remote), handleRemote);
  const media = occurrence.media.kind === "video"
    ? <video ref={video} className="player-content-media" src={occurrence.media.reference} autoPlay playsInline loop={occurrence.loop ?? false} muted={muted} onCanPlay={onReady} onError={onFailure} />
    : <img className="player-content-media" src={occurrence.media.reference} alt={occurrence.title} onLoad={onReady} onError={onFailure} />;
  return <section className="player-surface" aria-label="Cable player">
    <div className="player-toolbar"><button className="text-button" type="button" onClick={onBack}><span aria-hidden="true">←</span> Guide</button><span>{channel ? `${String(channel.number).padStart(2, "0")} ${channel.callsign}` : "CABLE"}</span></div>
    <div className="player-viewport" data-fit={occurrence.fit ?? "fit"}>
      {media}
      <div className="player-title"><p className="eyebrow">{formatInstant(occurrence.startsAt, presentation.revision.timezone, { dateStyle: "medium", timeStyle: undefined })}</p><h1>{occurrence.title}</h1><p>{occurrence.subtitle ?? "Cable Television"}</p></div>
      {opening && <div className="player-opening-overlay" role="status">Tuning</div>}
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
  if (state.kind === "error") return <ErrorState onRetry={() => dispatch({ type: "RETRY_REVISION" })} />;
  if (!("presentation" in state)) return <SyncState />;
  const occurrence = occurrenceForState(state);
  const isOffline = state.kind === "stale-offline" || state.presentation.revision.stale;
  const tune = () => {
    if (!occurrence) return;
    dispatch({ type: "SELECT_PROGRAM", occurrence });
    void navigate({ to: "/player" });
  };
  return <section className="placard-page"><div className="placard-orbit" aria-hidden="true" />{isOffline && <div className="state-banner state-banner-warning" role="status">Schedule may be out of date</div>}{occurrence ? <><p className="placard-channel">{state.presentation.channels.find((channel) => channel.id === occurrence.channelId)?.callsign ?? "CABLE"}</p><h1>{occurrence.title}</h1><p className="placard-subtitle">{occurrence.subtitle ?? "Now showing"}</p><div className="placard-meta"><span>{formatInstant(occurrence.startsAt, state.presentation.revision.timezone)}</span><span>Until {formatInstant(occurrence.endsAt, state.presentation.revision.timezone, { dateStyle: undefined, timeStyle: "short" })}</span></div></> : <><h1>Off air</h1><p className="placard-subtitle">There is no programme scheduled right now.</p></>}<div className="state-actions"><button className="button" type="button" disabled={!occurrence} onClick={tune}>Tune programme</button><button className="button secondary" type="button" onClick={() => { dispatch({ type: "BACK_TO_GUIDE" }); void navigate({ to: "/" }); }}>Guide</button></div></section>;
}
function DiagnosticsPage() {
  const { state, dispatch } = useRuntime();
  const navigate = useNavigate();
  if (state.kind === "error") return <ErrorState onRetry={() => dispatch({ type: "RETRY_REVISION" })} />;
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
