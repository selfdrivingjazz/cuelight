import { describe, expect, it } from "vitest";
import { presentationFixture } from "./presentation";
import { initialRuntimeState, runtimeReducer } from "./state";

describe("Cable runtime state", () => {
  it("loads a live revision then tunes through warming and ready", () => {
    const ready = runtimeReducer(initialRuntimeState, { type: "SYNC_REVISION", presentation: presentationFixture });
    expect(ready.kind).toBe("ready");
    const occurrence = presentationFixture.occurrences[0]!;
    const warming = runtimeReducer(ready, { type: "SELECT_PROGRAM", occurrence });
    expect(warming.kind).toBe("warming");
    const opening = runtimeReducer(warming, { type: "ARTIFACT_READY" });
    expect(opening.kind).toBe("player-opening");
    const playerReady = runtimeReducer(opening, { type: "PLAYER_READY" });
    expect(playerReady.kind).toBe("player-ready");
    const transitioned = runtimeReducer(playerReady, { type: "TUNE_LIVE", occurrence: presentationFixture.occurrences[1]! });
    expect(transitioned.kind).toBe("player-opening");
    expect(transitioned.kind === "player-opening" && transitioned.occurrence.id).toBe("program-2");
    const failed = runtimeReducer(opening, { type: "PLAYER_FAILED", message: "Artifact unavailable" });
    expect(failed.kind).toBe("player-failure");
    expect(runtimeReducer(failed, { type: "RETRY_PLAYER" }).kind).toBe("player-opening");
    const selected = runtimeReducer(ready, { type: "SELECT_CHANNEL", channelId: presentationFixture.channels[1]!.id });
    expect(selected.kind).toBe("ready");
    expect(selected.kind === "ready" && selected.selectedId).toBe("channel-2");
    const placard = runtimeReducer(selected, { type: "OPEN_PLACARD" });
    expect(placard.kind).toBe("ready");
    expect(placard.kind === "ready" && placard.mode).toBe("placard");
    expect(placard.kind === "ready" && placard.selectedId).toBe("channel-2");
  });

  it("swaps a renewed remote descriptor without leaving the current view", () => {
    const ready = runtimeReducer(initialRuntimeState, { type: "SYNC_REVISION", presentation: presentationFixture });
    const placard = runtimeReducer(runtimeReducer(ready, { type: "SELECT_CHANNEL", channelId: "channel-2" }), { type: "OPEN_PLACARD" });
    const remote = { url: "https://control.example/remote/two#token=t", showQr: true, expiresAt: "2026-10-04T00:00:00Z" };
    const renewed = runtimeReducer(placard, { type: "UPDATE_REMOTE", remote });
    expect(renewed.kind === "ready" && [renewed.mode, renewed.selectedId, renewed.presentation.remote]).toEqual(["placard", "channel-2", remote]);
    expect(runtimeReducer(initialRuntimeState, { type: "UPDATE_REMOTE", remote })).toBe(initialRuntimeState);
  });

  it("preserves stale offline state and supports retry", () => {
    const stale = { ...presentationFixture, revision: { ...presentationFixture.revision, stale: true } };
    const state = runtimeReducer(initialRuntimeState, { type: "SYNC_REVISION", presentation: stale });
    expect(state.kind).toBe("stale-offline");
    expect(runtimeReducer(state, { type: "TUNE_LIVE", occurrence: stale.occurrences[0]! }).kind).toBe("player-opening");
    expect(runtimeReducer(state, { type: "OPEN_PLACARD" }).kind).toBe("stale-offline");
    expect(runtimeReducer(state, { type: "BACK_TO_GUIDE" }).kind).toBe("stale-offline");
  });

  it("represents an empty lineup explicitly", () => {
    const empty = { ...presentationFixture, occurrences: [] };
    expect(runtimeReducer(initialRuntimeState, { type: "SYNC_REVISION", presentation: empty }).kind).toBe("empty-lineup");
  });

  it("represents load failures and retries without a reload", () => {
    const failed = runtimeReducer(initialRuntimeState, { type: "REVISION_FAILED", message: "The Cable guide could not be reached." });
    expect(failed).toEqual({ kind: "error", message: "The Cable guide could not be reached." });
    expect(runtimeReducer(failed, { type: "RETRY_REVISION" })).toEqual({ kind: "revision-loading" });
  });

  it("adopts live refreshes without leaving the current view", () => {
    const edited = { ...presentationFixture, channels: presentationFixture.channels.map((channel) => ({ ...channel, name: `${channel.name}!` })) };
    const ready = runtimeReducer(initialRuntimeState, { type: "SYNC_REVISION", presentation: presentationFixture });
    const selected = runtimeReducer(ready, { type: "SELECT_CHANNEL", channelId: "channel-2" });
    const refreshed = runtimeReducer(selected, { type: "REFRESH_REVISION", presentation: edited });
    expect(refreshed).toMatchObject({ kind: "ready", selectedId: "channel-2", presentation: edited });
    const playing = runtimeReducer(ready, { type: "TUNE_LIVE", occurrence: presentationFixture.occurrences[0]! });
    expect(runtimeReducer(playing, { type: "REFRESH_REVISION", presentation: edited })).toMatchObject({ kind: "player-opening", occurrence: { id: "program-1" }, presentation: edited });
    expect(runtimeReducer(ready, { type: "REFRESH_REVISION", presentation: { ...edited, occurrences: [] } }).kind).toBe("empty-lineup");
    expect(runtimeReducer({ kind: "error", message: "x" }, { type: "REFRESH_REVISION", presentation: edited }).kind).toBe("error");
  });
});
