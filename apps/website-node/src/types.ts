/** Wire types for the browser-node API (see api/openapi.yaml, tag browser-node). */

export type BrowserCredential = {
  nodeId: string;
  token: string;
  expiresAt: string;
  renewAfter: string;
  eventEpoch: string;
};

export type ClaimStarted = {
  claimId: string;
  code: string;
  displayCode: string;
  pollToken: string;
  expiresAt: string;
  pollIntervalSeconds: number;
  pairUrl?: string;
};

export type ClaimStatus =
  | { status: "pending"; expiresAt?: string }
  | { status: "claimed"; credential: BrowserCredential };

export type PresentationKind = "media" | "media-schedule" | "web";

export type Assignment = {
  id: string;
  nodeId: string;
  releaseId: string;
  releaseDigest: string;
  desiredGeneration: number;
  presentationKind?: PresentationKind;
  remoteInput?: boolean;
  showQr?: boolean;
};

export type PlaybackPayload = {
  fit: "fit" | "fill";
  loop: boolean;
  muted: boolean;
  volume: number;
  imageDurationSeconds?: number;
  orientation: number;
};

/** A schedule entry: a media artifact, or (with `web`) a live page shown for its duration. */
export type MediaScheduleItem = PlaybackPayload & {
  id: string;
  startsAt: string;
  endsAt: string;
  artifactDigest?: string;
  mediaType?: string;
  web?: { url: string };
};

export type ReleaseArtifact = { digest: string; size: number; mediaType: string };

export type ReleaseEnvelope = {
  id: string;
  digest: string;
  payload: {
    presentation: {
      kind: PresentationKind;
      media?: { playback: PlaybackPayload };
      mediaSchedule?: { timezone: string; items: MediaScheduleItem[] };
      web?: { url: string; appAccess?: boolean; mediaId?: string };
    };
    artifacts: ReleaseArtifact[];
  };
};

export type AppToken = { token: string; expiresAt: string; apiUrl: string };

export type RemoteSession = {
  sessionId: string;
  token: string;
  remoteUrl: string;
  showQr: boolean;
  expiresAt: string;
};

export type RemoteEvent = {
  sequence: number;
  type: "key" | "text" | "pointer" | "wheel";
  key?: string;
  text?: string;
  x?: number;
  y?: number;
  deltaX?: number;
  deltaY?: number;
  button?: "left" | "right" | "middle";
  state?: "move" | "down" | "up" | "click";
};

export type RemoteEventPage = { events: RemoteEvent[]; nextSequence: number };

export type ContentState = "playing" | "paused" | "off_air" | "stale";

export type NodeReport = {
  eventEpoch: string;
  sequence: number;
  desiredGenerationSeen: number;
  observedGeneration: number;
  presentationKind: PresentationKind | null;
  renderingReleaseDigest: string | null;
  candidateReleaseDigest: string | null;
  healthyReleaseDigest: string | null;
  lastKnownGoodReleaseDigest: string | null;
  currentContent?: {
    releaseDigest: string;
    presentationKind: PresentationKind;
    state: ContentState;
    transitionedAt: string;
    itemId?: string;
    positionMs?: number;
    width?: number;
    height?: number;
  };
  reportedAt: string;
  conditions: { type: string; status: boolean; reason: string; message?: string }[];
};

export type CapabilityReport = {
  platform: string;
  architecture: string;
  protocolVersion: string;
  rendererVersion: string;
  profileId: string;
  features: string[];
  acceptedMediaTypes: string[];
  outputReady: boolean;
  clockReliable: boolean;
};
