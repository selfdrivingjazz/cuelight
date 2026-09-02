import * as React from "react";
import type { components } from "@cable/api-client";
import { PreviewSurface } from "@cable/controller-ui";
import { controllerBaseUrl, getApiToken } from "../api";

type Media = components["schemas"]["Media"];

export interface AuthoringMediaPreviewProps {
  media?: Media;
  label: string;
  className?: string;
  aspectRatio?: React.CSSProperties["aspectRatio"];
  compact?: boolean;
}

type PreviewState =
  | { status: "empty" }
  | { status: "loading" }
  | { status: "ready"; url: string }
  | { status: "error"; message: string };


/** Fetches protected preview bytes with the active operator credential and owns the object URL lifecycle. */
export function AuthoringMediaPreview({ media, label, className, aspectRatio = "16 / 9", compact = false }: AuthoringMediaPreviewProps) {
  const token = getApiToken();
  const kind = media?.artifact.mediaType.startsWith("image/") ? "image" : media?.artifact.mediaType.startsWith("video/") ? "video" : null;
  const [preview, setPreview] = React.useState<PreviewState>(() => media ? { status: "loading" } : { status: "empty" });

  React.useEffect(() => {
    if (!media) {
      setPreview({ status: "empty" });
      return;
    }
    if (!kind) {
      setPreview({ status: "error", message: "This media type cannot be previewed in the browser." });
      return;
    }
    if (!token) {
      setPreview({ status: "error", message: "Sign in as an operator to load this preview." });
      return;
    }

    const controller = new AbortController();
    let objectUrl: string | undefined;
    setPreview({ status: "loading" });
    void fetch(`${controllerBaseUrl}/media/${encodeURIComponent(media.id)}/content`, {
      credentials: "omit",
      cache: "no-store",
      headers: { Authorization: `Bearer ${token}` },
      signal: controller.signal,
    }).then(async (response) => {
      if (!response.ok) throw new Error(`Preview request failed with status ${response.status}.`);
      const blob = await response.blob();
      if (controller.signal.aborted) return;
      objectUrl = URL.createObjectURL(blob);
      setPreview({ status: "ready", url: objectUrl });
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setPreview({ status: "error", message: error instanceof Error ? error.message : "The preview could not be loaded." });
    });

    return () => {
      controller.abort();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [kind, media, token]);

  const emptyDescription = media
    ? "No browser preview is available for this authored media."
    : "Media details have not been resolved for this item.";
  const surfaceClass = compact ? `min-h-20 ${className ?? ""}` : className;
  const readyMedia = preview.status === "ready" && kind
    ? kind === "image"
      ? { kind: "image" as const, src: preview.url, alt: label }
      : { kind: "video" as const, src: preview.url, label, muted: true, controls: true }
    : undefined;

  return <PreviewSurface
    aria-label={label}
    aspectRatio={aspectRatio}
    className={surfaceClass}
    fit={media?.playback.fit === "fill" ? "cover" : "contain"}
    media={readyMedia}
    state={preview.status === "ready" ? "ready" : preview.status}
    loadingLabel="Loading protected preview…"
    emptyTitle="Preview not resolved"
    emptyDescription={emptyDescription}
    errorTitle="Preview unavailable"
    errorDescription={preview.status === "error" ? preview.message : "The preview could not be loaded."}
  />;
}
