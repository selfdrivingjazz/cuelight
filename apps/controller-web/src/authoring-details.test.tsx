import * as React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import type { components } from "@cable/api-client";
import { describe, expect, it } from "vitest";
import {
  BlockInspector,
  ChannelInspector,
  PlaylistInspector,
  authoringIfMatch,
  webPlaylistHint,
  authoringLinks,
  formatDuration,
  formatTimestamp,
} from "./authoring-details";

type Playlist = components["schemas"]["CablePlaylist"];
type Block = components["schemas"]["CableBlock"];
type Channel = components["schemas"]["CableChannel"];

const playlist: Playlist = {
  id: "playlist-technical-id",
  name: "Morning loop",
  description: "Quiet opening rotation",
  version: 3,
  createdBy: "operator-1",
  createdAt: "2026-09-01T08:00:00Z",
  updatedAt: "2026-09-01T09:00:00Z",
  items: [
    { id: "item-2", playlistId: "playlist-technical-id", assetId: "asset-2", position: 2, durationSeconds: 65, title: "City walk", subtitle: "Northbound" },
    { id: "item-1", playlistId: "playlist-technical-id", assetId: "asset-1", position: 1, durationSeconds: 60, title: "First light", subtitle: "" },
  ],
};

const currentBlock = {
  id: "block-current",
  channelId: "channel-1",
  playlistId: playlist.id,
  title: "Morning loop",
  startsAt: "2026-09-02T09:00:00Z",
  endsAt: "2026-09-02T10:00:00Z",
  version: 2,
  createdBy: "operator-1",
  createdAt: "2026-09-01T08:00:00Z",
} as Block;

const nextBlock = {
  ...currentBlock,
  id: "block-next",
  playlistId: undefined,
  assetId: "asset-3",
  title: "Gallery hour",
  startsAt: "2026-09-02T10:00:00Z",
  endsAt: "2026-09-02T12:00:00Z",
} as Block;

const channel: Channel = {
  id: "channel-1",
  deploymentId: "deployment-1",
  number: 7,
  name: "Commons",
  callsign: "CMNS",
  description: "Shared spaces",
  accent: "slate",
  enabled: true,
  standby: false,
  version: 4,
  createdAt: "2026-09-01T08:00:00Z",
  updatedAt: "2026-09-01T09:00:00Z",
};

describe("Cable authoring detail workflows", () => {
  it("builds stable cross-links with encoded resource IDs", () => {
    expect(authoringLinks.playlist("playlist/one")).toEqual({ href: "/library/playlists/playlist%2Fone", label: "View playlist" });
    expect(authoringLinks.asset("asset one").href).toBe("/library/assets/asset%20one");
    expect(authoringLinks.release("r-1").href).toBe("/releases/r-1");
  });

  it("formats source timing without hiding invalid values", () => {
    expect(formatDuration(125)).toBe("2m 5s");
    expect(formatDuration(3_725)).toBe("1h 2m");
    expect(formatDuration(-1)).toBe("Unknown duration");
    expect(formatTimestamp("not-a-time")).toBe("Unknown time");
  });

  it("preserves authored playlist order and total runtime", () => {
    const markup = renderToStaticMarkup(<PlaylistInspector playlist={playlist} visualState="Visual enrichment is not connected on this route." />);

    expect(markup).toContain("2 items · 2m 5s total runtime");
    expect(markup.indexOf("First light")).toBeLessThan(markup.indexOf("City walk"));
    expect(markup).not.toContain(webPlaylistHint);
  });

  it("orders web page items with files and warns which screens can play them", () => {
    const withWeb = { ...playlist, items: [...playlist.items, { id: "item-3", playlistId: playlist.id, mediaId: "media-web", position: 3, durationSeconds: 30, title: "Status board", subtitle: "" }] };
    const markup = renderToStaticMarkup(<PlaylistInspector playlist={withWeb} />);

    expect(markup).toContain("3 items · 2m 35s total runtime");
    expect(markup.indexOf("City walk")).toBeLessThan(markup.indexOf("Status board"));
    expect(markup).toContain(webPlaylistHint);
  });

  it("gives an empty playlist a useful next step without rendering reorder controls", () => {
    const markup = renderToStaticMarkup(<PlaylistInspector playlist={{ ...playlist, items: [] }} />);

    expect(markup).toContain("Playlist is empty");
    expect(markup).not.toContain("Reorder");
  });

  it("exposes drag handles, inline duration, and removal for each item", () => {
    const markup = renderToStaticMarkup(<PlaylistInspector playlist={playlist} onAdd={() => undefined} onRemove={() => undefined} onDurationChange={() => undefined} onReorder={() => undefined} />);

    expect(markup).toContain("Add media");
    expect(markup).toContain("Reorder First light, position 1 of 2");
    expect(markup).toContain("Remove City walk");
    expect(markup).toContain("Duration for City walk");
  });

  it("uses version ETags as the conflict boundary for every authored replacement", () => {
    expect(authoringIfMatch(12)).toBe('"v12"');
  });

  it("keeps block source and timing visible when visual enrichment is unavailable", () => {
    const markup = renderToStaticMarkup(<BlockInspector block={currentBlock} playlist={playlist} visualState="Visual enrichment is not connected on this route." action={<button type="button">Edit block</button>} />);

    expect(markup).toContain("Schedule window");
    expect(markup).toContain("Edit block");
    expect(markup).toContain("1h 0m");
  });

  it("shows current, next, and proportional channel schedule hierarchy responsively", () => {
    const markup = renderToStaticMarkup(<ChannelInspector channel={channel} blocks={[nextBlock, currentBlock]} now={new Date("2026-09-02T09:30:00Z")} />);
    expect(markup).toContain("Open full block schedule");

    expect(markup).toContain("Scheduled now");
    expect(markup).toContain("Current and next");
    expect(markup).toContain("Morning loop");
    expect(markup).toContain("Gallery hour");
    expect(markup).toContain("Proportional channel schedule");
  });

  it("renders compact current and next empty states when the channel window is empty", () => {
    const markup = renderToStaticMarkup(<ChannelInspector channel={channel} blocks={[]} now={new Date("2026-09-02T09:30:00Z")} />);

    expect(markup).toContain("Nothing scheduled now");
    expect(markup).toContain("No next block");
    expect(markup).toContain("No scheduled blocks");
  });
});
