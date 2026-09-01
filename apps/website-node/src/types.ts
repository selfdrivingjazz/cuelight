export type PlaybackState =
  | "playing"
  | "autoplay_suspended"
  | "offline"
  | "unsupported";

export type NodeCredential = {
  nodeId: string;
  token: string;
  expiresAt: string;
};

export type Assignment = {
  id: string;
  nodeId: string;
  sequence: number;
  eventEpoch: string;
  mediaUrl: string;
  mediaType: string;
  durationMs?: number;
};

export type PlaybackReport = {
  assignmentId: string;
  eventEpoch: string;
  sequence: number;
  state: PlaybackState;
  positionMs: number;
  observedAt: string;
};

export type ClaimResult = NodeCredential;

export interface CredentialStorage {
  load(): NodeCredential | null;
  save(credential: NodeCredential): void;
  clear(): void;
}

export type AssignmentListener = (assignment: Assignment) => void;
export type TransportErrorListener = (error: unknown) => void;

export interface BrowserNodeTransport {
  claim(nodeId: string, code: string): Promise<ClaimResult>;
  renew(nodeId: string, token: string): Promise<ClaimResult>;
  subscribe(
    nodeId: string,
    token: string,
    afterSequence: number,
    onAssignment: AssignmentListener,
    onError: TransportErrorListener,
  ): () => void;
  acknowledge(nodeId: string, token: string, report: PlaybackReport): Promise<void>;
}

export interface MediaPort {
  showImage(url: string): Promise<void>;
  loadVideo(url: string): Promise<void>;
  playVideo(): Promise<void>;
  clear(): void;
}

const SUPPORTED_IMAGE_TYPES: Record<string, true> = {
  "image/avif": true,
  "image/gif": true,
  "image/jpeg": true,
  "image/png": true,
  "image/webp": true,
};
const SUPPORTED_VIDEO_TYPES: Record<string, true> = {
  "video/mp4": true,
  "video/webm": true,
  "video/ogg": true,
};

export function isSupportedMedia(mediaType: string, mediaUrl = ""): boolean {
  const normalized = mediaType.toLowerCase().split(";", 1)[0] ?? "";
  if (SUPPORTED_IMAGE_TYPES[normalized] || SUPPORTED_VIDEO_TYPES[normalized]) return true;
  // A transport may omit a content type for URLs that carry an unambiguous extension.
  if (!mediaType) return /\.(avif|gif|jpe?g|png|webp|mp4|webm|ogg)(?:[?#].*)?$/i.test(mediaUrl);
  return false;
}

export function isVideo(assignment: Assignment): boolean {
  const normalized = assignment.mediaType.toLowerCase().split(";", 1)[0] ?? "";
  return Boolean(SUPPORTED_VIDEO_TYPES[normalized]) || (!assignment.mediaType && /\.(mp4|webm|ogg)(?:[?#].*)?$/i.test(assignment.mediaUrl));
}
