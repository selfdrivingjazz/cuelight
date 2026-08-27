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
  });

  it("preserves stale offline state and supports retry", () => {
    const stale = { ...presentationFixture, revision: { ...presentationFixture.revision, stale: true } };
    const state = runtimeReducer(initialRuntimeState, { type: "SYNC_REVISION", presentation: stale });
    expect(state.kind).toBe("stale-offline");
    expect(runtimeReducer(state, { type: "TUNE_LIVE", occurrence: stale.occurrences[0]! }).kind).toBe("player-opening");
  });

  it("represents an empty lineup explicitly", () => {
    const empty = { ...presentationFixture, occurrences: [] };
    expect(runtimeReducer(initialRuntimeState, { type: "SYNC_REVISION", presentation: empty }).kind).toBe("empty-lineup");
  });
});
