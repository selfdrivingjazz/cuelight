import * as React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { components } from "@cable/api-client";
import {
  FleetNodeList,
  NodePlaybackView,
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
    channelId: "channel-9",
    blockId: "block-3",
    mediaId: "media-2",
    transitionedAt: "2026-09-02T11:55:00.000Z",
  } : undefined,
});

const media: Media = {
  id: "media-2",
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

    const nodes: FleetNodeVisual[] = [
      { node: node("lobby", "Lobby display"), report: report("playing") },
      { node: node("studio", "Studio display"), report: report("off_air") },
    ];
    const markup = renderToStaticMarkup(<FleetNodeList
      nodes={nodes}
      selectedNodeIds={["lobby"]}
      onSelectedNodeIdsChange={() => undefined}
      bulkActions={(selected) => <button type="button">Restart playback on {selected.length}</button>}
    />);

    expect(markup).toContain("Select all nodes");
    expect(markup).toContain("Select Lobby display");
    expect(markup).toContain("checked=\"\"");
    expect(markup).toContain("1 selected");
    expect(markup).toContain("Restart playback on 1");
    expect(markup).not.toMatch(/ssh/i);
  });

  it("makes mixed playing, off-air, stale, and older-node reports scannable", () => {
    const nodes: FleetNodeVisual[] = [
      { node: node("one", "Playing screen"), report: report("playing") },
      { node: node("two", "Off-air screen"), report: report("off_air") },
      { node: node("three", "Stale screen"), report: report("stale") },
      { node: node("four", "Older screen"), report: report() },
    ];
    const markup = renderToStaticMarkup(<FleetNodeList nodes={nodes} now={new Date("2026-09-02T12:00:00.000Z")} />);

    expect(markup).toContain("Playing");
    expect(markup).toContain("Off air");
    expect(markup).toContain("Stale");
    expect(markup).toContain("Observation unavailable");
    expect(markup).toContain("older player");
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
      observedNow={{ title: "Morning headlines", channelName: "Channel 9", blockTitle: "Morning block" }}
      expectedNow={{ title: "Morning headlines", media, mediaPreviewUrl: "blob:expected-media" }}
      next={{ title: "Weather", channelName: "Channel 9" }}
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
});
