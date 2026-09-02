import * as React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import type { components } from "@cable/api-client";
import { describe, expect, it } from "vitest";
import {
  BlockInspector,
  ChannelInspector,
  PlaylistInspector,
  ProfileInspector,
  authoringLinks,
  formatDuration,
  formatTimestamp,
} from "./authoring-details";

type Playlist = components["schemas"]["CablePlaylist"];
type Block = components["schemas"]["CableBlock"];
type Channel = components["schemas"]["CableChannel"];
type Profile = components["schemas"]["CableProfile"];
type UsagePage = components["schemas"]["UsagePage"];

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

const profile: Profile = {
  id: "profile-1",
  name: "Lobby",
  description: "Public lobby screens",
  deploymentId: "deployment-1",
  shellBuildId: "shell-1",
  releaseId: "release-1",
  version: 2,
  createdBy: "operator-1",
  createdAt: "2026-09-01T08:00:00Z",
  updatedAt: "2026-09-01T09:00:00Z",
};

describe("Cable authoring detail workflows", () => {
  it("builds stable cross-links with encoded resource IDs", () => {
    expect(authoringLinks.playlist("playlist/one")).toEqual({ href: "/cable/playlists/playlist%2Fone", label: "View playlist" });
    expect(authoringLinks.asset("asset one").href).toBe("/assets/asset%20one");
    expect(authoringLinks.release("r-1").href).toBe("/cable/releases/r-1");
  });

  it("formats source timing without hiding invalid values", () => {
    expect(formatDuration(125)).toBe("2m 5s");
    expect(formatDuration(3_725)).toBe("1h 2m");
    expect(formatDuration(-1)).toBe("Unknown duration");
    expect(formatTimestamp("not-a-time")).toBe("Unknown time");
  });

  it("renders a responsive playlist mosaic before compact ordered rows", () => {
    const markup = renderToStaticMarkup(<PlaylistInspector playlist={playlist} visualState="Visual enrichment is not connected on this route." />);

    expect(markup).toContain("At a glance");
    expect(markup).toContain("2 items · 2m 5s total runtime");
    expect(markup).toContain("Morning loop media mosaic");
    expect(markup).toContain("sm:grid-cols-2 lg:grid-cols-4");
    expect(markup).toContain("sm:grid-cols-[2rem_8rem_minmax(0,1fr)_auto]");
    expect(markup.indexOf("First light")).toBeLessThan(markup.indexOf("City walk"));
    expect(markup.indexOf("At a glance")).toBeLessThan(markup.indexOf("Play order"));
  });

  it("gives an empty playlist a useful next step without rendering reorder controls", () => {
    const markup = renderToStaticMarkup(<PlaylistInspector playlist={{ ...playlist, items: [] }} />);

    expect(markup).toContain("Playlist is empty");
    expect(markup).toContain("Add authored media before placing this playlist in a scheduled block.");
    expect(markup).not.toContain("Move up");
  });

  it("keeps block source and timing ahead of an honest compiled-data gap", () => {
    const markup = renderToStaticMarkup(<BlockInspector block={currentBlock} playlist={playlist} visualState="Visual enrichment is not connected on this route." />);

    expect(markup).toContain("Schedule window");
    expect(markup).toContain("1h 0m");
    expect(markup).toContain("Compiled media sequence");
    expect(markup).toContain("Showing the authored playlist order instead");
    expect(markup.indexOf("Schedule window")).toBeLessThan(markup.indexOf("Compiled media sequence"));
    expect(markup).toContain("sm:grid-cols-2 lg:grid-cols-4");
  });

  it("shows current, next, and proportional channel schedule hierarchy responsively", () => {
    const markup = renderToStaticMarkup(<ChannelInspector channel={channel} blocks={[nextBlock, currentBlock]} now={new Date("2026-09-02T09:30:00Z")} />);

    expect(markup).toContain("Scheduled now");
    expect(markup).toContain("Current and next");
    expect(markup).toContain("Morning loop");
    expect(markup).toContain("Gallery hour");
    expect(markup).toContain("Proportional channel schedule");
    expect(markup).toContain("md:grid-cols-2");
    expect(markup).toContain("flex-col");
    expect(markup).toContain("sm:flex-row");
    expect(markup.indexOf("Current and next")).toBeLessThan(markup.indexOf("Schedule timeline"));
  });

  it("renders compact current and next empty states when the channel window is empty", () => {
    const markup = renderToStaticMarkup(<ChannelInspector channel={channel} blocks={[]} now={new Date("2026-09-02T09:30:00Z")} />);

    expect(markup).toContain("Nothing scheduled now");
    expect(markup).toContain("No next block");
    expect(markup).toContain("No scheduled blocks");
  });

  it("summarizes linked release and assigned players from injected usage references", () => {
    const usage: UsagePage = { resourceId: profile.id, references: [{ kind: "release", id: "release-1", name: "Lobby release", href: "/releases/release-1" }, { kind: "assignment", id: "node-1", name: "North lobby", href: "/nodes/node-1" }], hasMore: false };
    const markup = renderToStaticMarkup(<ProfileInspector profile={profile} channels={[channel]} visuals={{ usage }} />);

    expect(markup).toContain("Representative lineup");
    expect(markup).toContain("Lobby release");
    expect(markup).toContain("1 assigned player");
    expect(markup).toContain("North lobby");
    expect(markup).toContain("sm:grid-cols-2 lg:grid-cols-4");
  });

  it("is honest when profile channels and usage are unavailable", () => {
    const markup = renderToStaticMarkup(<ProfileInspector profile={{ ...profile, releaseId: undefined }} channels={[]} visualState="Assignment usage is not connected on this route." />);

    expect(markup).toContain("No representative channels");
    expect(markup).toContain("No release linked");
    expect(markup).toContain("Assignment usage is not connected on this route.");
  });
});
