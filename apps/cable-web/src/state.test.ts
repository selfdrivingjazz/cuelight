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
    expect(runtimeReducer(opening, { type: "PLAYER_READY" }).kind).toBe("player-ready");
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

  it("represents bridge failures and retries without a reload", () => {
    const failed = runtimeReducer(initialRuntimeState, { type: "REVISION_FAILED", message: "The node bridge could not be reached." });
    expect(failed).toEqual({ kind: "error", message: "The node bridge could not be reached." });
    expect(runtimeReducer(failed, { type: "RETRY_REVISION" })).toEqual({ kind: "revision-loading" });
  });
});
