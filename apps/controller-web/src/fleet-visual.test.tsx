import * as React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { components } from "@cuelight/api-client";
import {
  NodePlaybackView,
  ScreenTile,
  nextNodeSelection,
  type FleetNodeVisual,
  type Media,
  type NodeReport,
} from "./fleet-visual";

type Node = components["schemas"]["Node"];

const node = (id: string, name: string): Node => ({
  id,
  name,
  credentialState: "active",
  desiredGeneration: 7,
  observedGeneration: 7,
});

const report = (state?: "playing" | "paused" | "off_air" | "stale"): NodeReport => ({
  eventEpoch: "38bb21bf-5d69-48ee-923c-70f09ec4ef3b",
  sequence: 42,
  desiredGenerationSeen: 7,
  observedGeneration: 7,
  presentationKind: state ? "media-schedule" : null,
  renderingReleaseDigest: state ? "release-digest" : null,
  conditions: [],
  currentContent: state ? {
    releaseDigest: "release-digest",
    presentationKind: "media-schedule",
    state,
    mediaId: "media-2",
    transitionedAt: "2026-09-02T11:55:00.000Z",
  } : undefined,
});

const media: Media = {
  id: "media-2",
  kind: "file",
  appAccess: false,
  assetId: "asset-2",
  title: "Morning headlines",
  tags: ["news"],
  playback: { fit: "fit", loop: true, muted: true, volume: 0, orientation: 0 },
  artifact: { digest: "media-digest", size: 128_000, mediaType: "image/png", state: "ready" },
  version: 1,
  createdBy: "operator-1",
  updatedBy: "operator-1",
  createdAt: "2026-09-01T12:00:00.000Z",
  updatedAt: "2026-09-01T12:00:00.000Z",
};

describe("fleet visual playback surfaces", () => {
  it("keeps controlled node selection and bulk actions accessible", () => {
    expect(nextNodeSelection(["lobby"], "studio", true)).toEqual(["lobby", "studio"]);
    expect(nextNodeSelection(["lobby", "studio"], "lobby", false)).toEqual(["studio"]);

    const markup = renderToStaticMarkup(<ScreenTile node={node("lobby", "Lobby display")} report={report("playing")} nodeHref="/screens/lobby" selected onSelectedChange={() => undefined} />);
    expect(markup).toContain("Select Lobby display");
    expect(markup).toContain("checked=\"\"");
    expect(markup).toContain("href=\"/screens/lobby\"");
  });

  it("makes mixed playing, off-air, stale, and older-node reports scannable", () => {
    const tiles = [
      [report("playing"), "Playing"],
      [report("off_air"), "Off air"],
      [report("stale"), "Stale"],
      [report(), "Observation unavailable"],
    ] as const;
    for (const [nodeReport, label] of tiles) {
      expect(renderToStaticMarkup(<ScreenTile node={node("one", "Screen")} report={nodeReport} now={new Date("2026-09-02T12:00:00.000Z")} />)).toContain(label);
    }
  });

  it("does not turn expected content into an observation for older nodes", () => {
    const markup = renderToStaticMarkup(<NodePlaybackView
      node={node("older", "Older display")}
      report={report()}
      expectedNow={{ title: "Scheduled welcome loop" }}
    />);

    expect(markup).toContain("Playback observation unavailable");
    expect(markup).toContain("expected content is not treated as observed");
    expect(markup).toContain("Scheduled welcome loop");
    expect(markup).not.toContain("Now playing");
  });

  it("separates an expected media preview from an unavailable actual capture", () => {
    const markup = renderToStaticMarkup(<NodePlaybackView
      node={node("lobby", "Lobby display")}
      report={report("playing")}
      observedNow={{ title: "Morning headlines" }}
      expectedNow={{ title: "Morning headlines", media, mediaPreviewUrl: "blob:expected-media" }}
      next={{ title: "Weather" }}
    />);

    expect(markup).toContain("Expected now");
    expect(markup).toContain("blob:expected-media");
    expect(markup).toContain("Preview of Morning headlines");
    expect(markup).toContain("Actual capture");
    expect(markup).toContain("No capture exists");
    expect(markup).toContain("No image is inferred from its playback report");
    expect(markup).toContain("Up next");
    expect(markup).toContain("Weather");
    expect(markup).not.toContain("Actual capture from Lobby display");
  });
  it("labels which evidence a screen tile is showing, preferring a real capture", () => {
    const base: FleetNodeVisual = { node: node("lobby", "Lobby display"), report: report("playing"), observedNow: { title: "Reported title" }, expectedNow: { title: "Expected title", media, mediaPreviewUrl: "blob:expected" } };
    const captured = renderToStaticMarkup(<ScreenTile {...base} capture={{ url: "blob:actual", capturedAt: "2026-09-02T12:00:00Z" }} />);
    expect(captured).toContain("Capture preview for Lobby display");
    expect(captured).toContain("blob:actual");
    expect(captured).not.toContain("blob:expected");
    const expected = renderToStaticMarkup(<ScreenTile {...base} />);
    expect(expected).toContain("Expected preview for Lobby display");
    expect(expected).toContain("Reported title");
    const blank = renderToStaticMarkup(<ScreenTile node={node("dark", "Dark display")} />);
    expect(blank).toContain("No signal");
  });
});
