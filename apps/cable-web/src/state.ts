import type { Occurrence, Presentation } from "./presentation";

export type RuntimeState =
  | { kind: "boot" }
  | { kind: "revision-loading" }
  | { kind: "error"; message: string }
  | { kind: "ready"; presentation: Presentation; selectedId: string | null; mode: "guide" | "player" | "placard" }
  | { kind: "stale-offline"; presentation: Presentation; selectedId: string | null }
  | { kind: "empty-lineup"; presentation: Presentation }
  | { kind: "warming"; presentation: Presentation; occurrence: Occurrence }
  | { kind: "player-opening"; presentation: Presentation; occurrence: Occurrence }
  | { kind: "player-ready"; presentation: Presentation; occurrence: Occurrence }
  | { kind: "player-failure"; presentation: Presentation; occurrence: Occurrence; message: string }
  | { kind: "diagnostics"; presentation: Presentation };
 
export type RuntimeAction =
  | { type: "START_REVISION_LOAD" }
  | { type: "REVISION_FAILED"; message: string }
  | { type: "RETRY_REVISION" }
  | { type: "SYNC_REVISION"; presentation: Presentation }
  | { type: "REFRESH_REVISION"; presentation: Presentation }
  | { type: "UPDATE_REMOTE"; remote: NonNullable<Presentation["remote"]> }
  | { type: "SELECT_CHANNEL"; channelId: string }
  | { type: "SELECT_PROGRAM"; occurrence: Occurrence }
  | { type: "TUNE_LIVE"; occurrence: Occurrence }
  | { type: "ARTIFACT_READY" }
  | { type: "PLAYER_READY" }
  | { type: "PLAYER_FAILED"; message: string }
  | { type: "RETRY_PLAYER" }
  | { type: "BACK_TO_GUIDE" }
  | { type: "OPEN_PLACARD" }
  | { type: "OPEN_DIAGNOSTICS" };

export const initialRuntimeState: RuntimeState = { kind: "boot" };

export function runtimeReducer(state: RuntimeState, action: RuntimeAction): RuntimeState {
  switch (action.type) {
    case "START_REVISION_LOAD":
    case "RETRY_REVISION":
      return { kind: "revision-loading" };
    case "REVISION_FAILED":
      return { kind: "error", message: action.message };
    case "SYNC_REVISION":
      return action.presentation.occurrences.length === 0
        ? { kind: "empty-lineup", presentation: action.presentation }
        : action.presentation.bridge.state !== "online" || action.presentation.revision.stale
          ? { kind: "stale-offline", presentation: action.presentation, selectedId: null }
          : { kind: "ready", presentation: action.presentation, selectedId: null, mode: "guide" };
    case "REFRESH_REVISION": {
      // Periodic live refresh: adopt programming edits without leaving the current view.
      if (!("presentation" in state)) return state;
      if (state.kind === "ready" || state.kind === "stale-offline" || state.kind === "empty-lineup") {
        const synced = runtimeReducer(state, { type: "SYNC_REVISION", presentation: action.presentation });
        const selectedId = "selectedId" in state ? state.selectedId : null;
        if (synced.kind === "ready") return { ...synced, selectedId, mode: state.kind === "ready" ? state.mode : "guide" };
        if (synced.kind === "stale-offline") return { ...synced, selectedId };
        return synced;
      }
      return { ...state, presentation: action.presentation };
    }
    case "UPDATE_REMOTE":
      // The node renewed its remote session; keep the current view.
      if (!("presentation" in state)) return state;
      return { ...state, presentation: { ...state.presentation, remote: action.remote } };
    case "SELECT_CHANNEL":
      if (state.kind !== "ready" && state.kind !== "stale-offline") return state;
      return { ...state, selectedId: action.channelId };
    case "SELECT_PROGRAM":
      if (state.kind !== "ready" && state.kind !== "stale-offline") return state;
      return { kind: "warming", presentation: state.presentation, occurrence: action.occurrence };
    case "TUNE_LIVE":
      if (!("presentation" in state)) return state;
      return { kind: "player-opening", presentation: state.presentation, occurrence: action.occurrence };
    case "ARTIFACT_READY":
      if (state.kind !== "warming") return state;
      return { kind: "player-opening", presentation: state.presentation, occurrence: state.occurrence };
    case "PLAYER_READY":
      if (state.kind !== "player-opening") return state;
      return { kind: "player-ready", presentation: state.presentation, occurrence: state.occurrence };
    case "PLAYER_FAILED":
      if (state.kind !== "player-opening" && state.kind !== "player-ready") return state;
      return { kind: "player-failure", presentation: state.presentation, occurrence: state.occurrence, message: action.message };
    case "RETRY_PLAYER":
      if (state.kind !== "player-failure") return state;
      return { kind: "player-opening", presentation: state.presentation, occurrence: state.occurrence };
    case "BACK_TO_GUIDE":
      if (!("presentation" in state)) return state;
      if (state.kind === "stale-offline") return state;
      return { kind: "ready", presentation: state.presentation, selectedId: null, mode: "guide" };
    case "OPEN_PLACARD":
      if (!("presentation" in state)) return state;
      if (state.kind === "stale-offline") return state;
      return {
        kind: "ready",
        presentation: state.presentation,
        selectedId: state.kind === "ready" ? state.selectedId : null,
        mode: "placard",
      };
    case "OPEN_DIAGNOSTICS":
      if (!("presentation" in state)) return state;
      return { kind: "diagnostics", presentation: state.presentation };
  }
}
