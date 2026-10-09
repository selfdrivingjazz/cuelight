import type { PlaybackPayload, PresentationKind, ReleaseEnvelope } from "./types";

export type MediaItem = PlaybackPayload & {
  id: string;
  url: string;
  mediaType: string;
  element: "image" | "video";
  startsAt?: number;
  endsAt?: number;
};

/** A live web page shown in a frame for its scheduled duration. */
export type WebItem = { id: string; webUrl: string; startsAt: number; endsAt: number };

export type ScheduleItem = MediaItem | WebItem;

export type Presentation =
  | { kind: "media"; releaseDigest: string; item: MediaItem }
  | { kind: "media-schedule"; releaseDigest: string; items: ScheduleItem[] }
  /**
   * `appAccess` pages get an app token for `mediaId` in their URL fragment and read
   * remote input themselves; the node neither forwards remote events nor shows its QR.
   */
  | { kind: "web"; releaseDigest: string; url: string; appAccess: boolean; mediaId?: string }
  | { kind: "unsupported"; releaseDigest: string; presentationKind: PresentationKind; reason: string };

export function isWebItem(item: ScheduleItem): item is WebItem {
  return "webUrl" in item;
}

/** Maps a signed release to what a plain browser can render. */
export function toPresentation(release: ReleaseEnvelope, artifactUrl: (digest: string) => string): Presentation {
  const presentation = release.payload.presentation;
  const releaseDigest = release.digest;
  if (presentation.kind === "media" && presentation.media) {
    const artifact = release.payload.artifacts[0];
    const element = artifact ? mediaElement(artifact.mediaType) : null;
    if (!artifact || !element) return { kind: "unsupported", releaseDigest, presentationKind: "media", reason: `Unsupported media type ${artifact ? artifact.mediaType : "(none)"}` };
    return { kind: "media", releaseDigest, item: { ...presentation.media.playback, id: artifact.digest, url: artifactUrl(artifact.digest), mediaType: artifact.mediaType, element } };
  }
  if (presentation.kind === "media-schedule" && presentation.mediaSchedule) {
    const items: ScheduleItem[] = [];
    for (const item of presentation.mediaSchedule.items) {
      const startsAt = Date.parse(item.startsAt);
      const endsAt = Date.parse(item.endsAt);
      if (item.web) {
        if (/^https?:\/\//i.test(item.web.url)) items.push({ id: item.id, webUrl: item.web.url, startsAt, endsAt });
        continue;
      }
      const element = item.mediaType ? mediaElement(item.mediaType) : null;
      if (!element || !item.artifactDigest || !item.mediaType) continue;
      items.push({ ...item, url: artifactUrl(item.artifactDigest), mediaType: item.mediaType, element, startsAt, endsAt });
    }
    if (items.length === 0) return { kind: "unsupported", releaseDigest, presentationKind: "media-schedule", reason: "No schedule item is playable in a browser" };
    items.sort((a, b) => (a.startsAt || 0) - (b.startsAt || 0));
    return { kind: "media-schedule", releaseDigest, items };
  }
  if (presentation.kind === "web" && presentation.web && /^https?:\/\//i.test(presentation.web.url)) {
    const { url, appAccess, mediaId } = presentation.web;
    if (appAccess && !mediaId) return { kind: "unsupported", releaseDigest, presentationKind: "web", reason: "App access page has no media id" };
    return appAccess ? { kind: "web", releaseDigest, url, appAccess: true, mediaId } : { kind: "web", releaseDigest, url, appAccess: false };
  }
  return { kind: "unsupported", releaseDigest, presentationKind: presentation.kind, reason: "Release presentation is incomplete" };
}

function mediaElement(mediaType: string): "image" | "video" | null {
  const type = mediaType.toLowerCase().split(";")[0] || "";
  if (type.indexOf("image/") === 0) return "image";
  if (type.indexOf("video/") === 0) return "video";
  return null;
}

/**
 * Appends the app token fragment params to `pageUrl`, keeping its other fragment
 * params and dropping stale `cuelight-token`/`cuelight-api` ones.
 */
export function withAppToken(pageUrl: string, token: string, apiUrl: string): string {
  const hash = pageUrl.indexOf("#");
  const base = hash < 0 ? pageUrl : pageUrl.slice(0, hash);
  const kept = hash < 0 ? [] : pageUrl.slice(hash + 1).split("&").filter((part) => {
    const name = part.split("=")[0];
    return part !== "" && name !== "cuelight-token" && name !== "cuelight-api";
  });
  kept.push(`cuelight-token=${encodeURIComponent(token)}`, `cuelight-api=${encodeURIComponent(apiUrl)}`);
  return `${base}#${kept.join("&")}`;
}

/** The schedule item airing at `now`, or null when the schedule is off air. */
export function scheduledItem<T extends { startsAt?: number; endsAt?: number }>(items: T[], now: number): T | null {
  for (const item of items) {
    if ((item.startsAt || 0) <= now && now < (item.endsAt || 0)) return item;
  }
  return null;
}

/** Milliseconds until the airing item changes, or null when nothing else is scheduled. */
export function nextScheduleChange(items: { startsAt?: number; endsAt?: number }[], now: number): number | null {
  let next = Infinity;
  for (const item of items) {
    if ((item.startsAt || 0) > now) next = Math.min(next, item.startsAt || 0);
    if ((item.endsAt || 0) > now) next = Math.min(next, item.endsAt || 0);
  }
  return next === Infinity ? null : next - now;
}
