import * as React from "react";
import type { components } from "@cuelight/api-client";
import { MediaThumbnail } from "../media-thumbnail";

type Media = components["schemas"]["Media"];

export interface AuthoringMediaPreviewProps {
  media?: Media;
  label: string;
  className?: string;
  aspectRatio?: React.CSSProperties["aspectRatio"];
  compact?: boolean;
}

/** Authoring cards show the persisted poster; originals load only on the media detail page. */
export function AuthoringMediaPreview({ media, label, className, aspectRatio = "16 / 9", compact = false }: AuthoringMediaPreviewProps) {
  return <MediaThumbnail media={media} label={label} aspectRatio={aspectRatio} className={compact ? `min-h-20 ${className ?? ""}` : className} />;
}
