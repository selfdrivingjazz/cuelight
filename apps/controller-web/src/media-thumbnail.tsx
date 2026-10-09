import * as React from "react";
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import type { components } from "@cuelight/api-client";
import { PreviewSurface } from "@cuelight/controller-ui";
import { ApiRequestError, controllerBaseUrl, getApiToken, hasCapability } from "./api";
import { useAuth } from "./auth";

type Media = components["schemas"]["Media"];

const posterMaxEdge = 640;

function mediaUrl(media: Media, suffix: string): string {
  return `${controllerBaseUrl.replace(/\/$/, "")}/media/${encodeURIComponent(media.id)}/${suffix}`;
}

function authHeaders(accept?: string): Headers {
  const headers = new Headers();
  if (accept) headers.set("Accept", accept);
  const token = getApiToken();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  return headers;
}

export function previewKind(mediaType: string): "image" | "video" | null {
  if (mediaType.startsWith("image/")) return "image";
  if (mediaType.startsWith("video/")) return "video";
  return null;
}

/**
 * Persisted posters are content-addressed: the `?v=digest` URL is served
 * immutable, so the browser HTTP cache answers reloads without a round trip.
 */
async function fetchThumbnailObjectUrl(media: Media, signal: AbortSignal): Promise<string> {
  const digest = media.thumbnail?.digest;
  if (!digest) throw new Error("This media has no thumbnail yet.");
  const response = await fetch(`${mediaUrl(media, "thumbnail")}?v=${digest}`, { headers: authHeaders(media.kind === "web" ? "image/png" : "image/jpeg"), credentials: "omit", signal });
  if (!response.ok) throw new ApiRequestError(response.status, "The thumbnail could not be fetched.");
  return URL.createObjectURL(await response.blob());
}

/** Downloads the original once, draws a poster frame, and uploads it so no client has to decode it again. */
async function capturePoster(media: Media, queryClient: QueryClient, signal: AbortSignal): Promise<null> {
  const artifact = media.artifact!;
  const kind = previewKind(artifact.mediaType);
  const response = await fetch(mediaUrl(media, "content"), { headers: authHeaders(artifact.mediaType), credentials: "omit", signal });
  if (!response.ok) throw new ApiRequestError(response.status, "The original could not be fetched to build a thumbnail.");
  const source = URL.createObjectURL(await response.blob());
  try {
    let element: HTMLImageElement | HTMLVideoElement;
    let width: number;
    let height: number;
    if (kind === "video") {
      const video = document.createElement("video");
      video.muted = true;
      video.preload = "auto";
      video.src = source;
      await new Promise<void>((resolve, reject) => {
        video.onloadeddata = () => resolve();
        video.onerror = () => reject(new Error("The video could not be decoded."));
      });
      // Skip fade-ins: a quarter of the way in, capped at one second.
      video.currentTime = Math.min(1, (video.duration || 0) / 4);
      await new Promise<void>((resolve) => { video.onseeked = () => resolve(); });
      element = video;
      width = video.videoWidth;
      height = video.videoHeight;
    } else {
      const image = new Image();
      image.src = source;
      await image.decode();
      element = image;
      width = image.naturalWidth;
      height = image.naturalHeight;
    }
    if (!width || !height) throw new Error("The media has no visible frame.");
    const scale = Math.min(1, posterMaxEdge / Math.max(width, height));
    const canvas = document.createElement("canvas");
    canvas.width = Math.max(1, Math.round(width * scale));
    canvas.height = Math.max(1, Math.round(height * scale));
    canvas.getContext("2d")?.drawImage(element, 0, 0, canvas.width, canvas.height);
    const poster = await new Promise<Blob>((resolve, reject) => canvas.toBlob((blob) => blob ? resolve(blob) : reject(new Error("The poster could not be encoded.")), "image/jpeg", 0.85));
    const upload = await fetch(mediaUrl(media, "thumbnail"), { method: "PUT", headers: (() => { const headers = authHeaders(); headers.set("Content-Type", "image/jpeg"); return headers; })(), body: poster, credentials: "omit", signal });
    if (!upload.ok) throw new ApiRequestError(upload.status, "The captured thumbnail could not be saved.");
    await queryClient.invalidateQueries({ queryKey: ["controller", "media"] });
    await queryClient.invalidateQueries({ queryKey: ["controller", "media-detail", media.id] });
    return null;
  } finally {
    URL.revokeObjectURL(source);
  }
}

export interface MediaThumbnailState {
  status: "loading" | "ready" | "empty" | "error";
  url?: string;
}

/**
 * Resolves the persisted poster for media. Web pages use the controller's page
 * capture. When the controller could not generate a file poster (video, WebP),
 * an operator with media_write captures it once.
 */
export function useMediaThumbnail(media?: Media): MediaThumbnailState {
  const queryClient = useQueryClient();
  const auth = useAuth();
  const digest = media?.thumbnail?.digest;
  const fileKind = media?.artifact ? previewKind(media.artifact.mediaType) : null;
  const canCapture = Boolean(media?.artifact && !digest && fileKind && media.artifact.state !== "missing" && media.artifact.state !== "quarantined"
    && auth.operator && hasCapability(auth.operator, "media_write", "media_write"));
  const thumbnail = useQuery({
    queryKey: ["media-thumbnail", digest],
    queryFn: ({ signal }) => fetchThumbnailObjectUrl(media as Media, signal),
    enabled: Boolean(digest),
    staleTime: Infinity,
    gcTime: Infinity,
  });
  const capture = useQuery({
    queryKey: ["media-poster-capture", media?.artifact?.digest],
    queryFn: ({ signal }) => capturePoster(media as Media, queryClient, signal),
    enabled: canCapture,
    staleTime: Infinity,
    gcTime: Infinity,
    retry: false,
  });
  if (!media || (media.kind !== "web" && !fileKind)) return { status: "empty" };
  if (digest) return thumbnail.data ? { status: "ready", url: thumbnail.data } : { status: thumbnail.isError ? "error" : "loading" };
  if (media.kind === "web") return { status: media.thumbnailError ? "empty" : "loading" };
  if (!canCapture || capture.isError) return { status: "empty" };
  return { status: "loading" };
}

export interface MediaThumbnailProps {
  media?: Media;
  className?: string;
  label?: string;
  aspectRatio?: React.CSSProperties["aspectRatio"];
  fit?: "contain" | "cover";
  overlay?: React.ReactNode;
}

/** Static poster for cards, pickers, and tiles. Never downloads the original unless a poster must be captured. */
export function MediaThumbnail({ media, className, label, aspectRatio = "16 / 9", fit, overlay }: MediaThumbnailProps) {
  const thumbnail = useMediaThumbnail(media);
  const title = label ?? (media ? `${media.title} thumbnail` : "Media thumbnail");
  return <PreviewSurface
    aria-label={title}
    aspectRatio={aspectRatio}
    className={className}
    fit={fit ?? (media?.kind === "web" || media?.playback?.fit === "fill" ? "cover" : "contain")}
    media={thumbnail.url ? { kind: "image", src: thumbnail.url, alt: title, loading: "lazy" } : undefined}
    state={thumbnail.status}
    loadingLabel={media?.kind === "web" ? "Capturing page…" : "Preparing thumbnail…"}
    emptyTitle={media?.kind === "web" ? (media.thumbnailError ? "Capture failed" : "No capture yet") : media?.artifact ? media.artifact.mediaType : "Preview not resolved"}
    emptyDescription={media?.kind === "web" ? media.thumbnailError ?? media.url ?? "" : media ? "No thumbnail is available for this media." : "Media details have not been resolved for this item."}
    errorDescription="The thumbnail could not be loaded."
    overlay={overlay}
  />;
}
