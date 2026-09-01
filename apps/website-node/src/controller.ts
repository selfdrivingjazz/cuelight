import type {
  Assignment,
  BrowserNodeTransport,
  CredentialStorage,
  MediaPort,
  NodeCredential,
  PlaybackReport,
  PlaybackState,
} from "./types";
import { isSupportedMedia, isVideo } from "./types";

export type BrowserNodeStatus =
  | "unclaimed"
  | "connecting"
  | "ready"
  | PlaybackState
  | "error";

export type BrowserNodeSnapshot = {
  status: BrowserNodeStatus;
  credential: NodeCredential | null;
  assignment: Assignment | null;
  error: unknown;
};

type Listener = (snapshot: BrowserNodeSnapshot) => void;

export class BrowserNodeController {
  private credentialValue: NodeCredential | null = null;
  private assignmentValue: Assignment | null = null;
  private media: MediaPort;
  private unsubscribe: (() => void) | null = null;
  private listeners = new Set<Listener>();
  private statusValue: BrowserNodeStatus = "unclaimed";
  private errorValue: unknown = null;
  private latestSequence = 0;
  private operation = 0;

  constructor(
    private readonly transport: BrowserNodeTransport,
    private readonly storage: CredentialStorage,
    media: MediaPort,
  ) {
    this.media = media;
  }

  setMediaPort(media: MediaPort): void {
    this.media = media;
  }

  get snapshot(): BrowserNodeSnapshot {
    return { status: this.statusValue, credential: this.credentialValue, assignment: this.assignmentValue, error: this.errorValue };
  }
  subscribe(listener: Listener): () => void { this.listeners.add(listener); return () => this.listeners.delete(listener); }
  disconnect(): void {
    this.operation += 1;
    this.unsubscribe?.();
    this.unsubscribe = null;
  }

  async start(nodeId?: string): Promise<BrowserNodeSnapshot> {
    const saved = this.storage.load();
    if (!saved || !saved.nodeId || !saved.token) {
      this.setStatus("unclaimed");
      return this.snapshot;
    }
    if (Date.parse(saved.expiresAt) <= Date.now()) {
      this.storage.clear();
      this.setStatus("unclaimed");
      return this.snapshot;
    }
    if (nodeId && nodeId !== saved.nodeId) {
      // A URL cannot silently move a credential to another node identity.
      this.setError(new Error("stored credential is bound to another node"));
      return this.snapshot;
    }
    this.credentialValue = saved;
    await this.connect();
    return this.snapshot;
  }

  async claim(nodeId: string, code: string): Promise<BrowserNodeSnapshot> {
    this.setStatus("connecting");
    try {
      const credential = await this.transport.claim(nodeId, code.trim());
      this.storage.save(credential);
      this.credentialValue = credential;
      await this.connect();
      return this.snapshot;
    } catch (error) {
      this.setError(error);
      throw error;
    }
  }

  async renew(): Promise<BrowserNodeSnapshot> {
    const credential = this.requireCredential();
    try {
      const renewed = await this.transport.renew(credential.nodeId, credential.token);
      this.storage.save(renewed);
      this.credentialValue = renewed;
      this.setStatus(this.assignmentValue ? this.statusValue : "ready");
      return this.snapshot;
    } catch (error) {
      this.setError(error);
      throw error;
    }
  }

  signOut(): void {
    this.operation += 1;
    this.unsubscribe?.();
    this.unsubscribe = null;
    this.storage.clear();
    this.credentialValue = null;
    this.assignmentValue = null;
    this.latestSequence = 0;
    this.media.clear();
    this.setStatus("unclaimed");
  }

  async resumeAutoplay(): Promise<BrowserNodeSnapshot> {
    const assignment = this.assignmentValue;
    const credential = this.credentialValue;
    if (!assignment || !credential || !isVideo(assignment)) return this.snapshot;
    try {
      await this.media.playVideo();
      await this.report("playing", 0, assignment, credential);
      this.setStatus("playing");
    } catch (error) {
      await this.report("autoplay_suspended", 0, assignment, credential);
      this.setStatus("autoplay_suspended");
      this.errorValue = error;
      this.notify();
    }
    return this.snapshot;
  }

  private async connect(): Promise<void> {
    const credential = this.requireCredential();
    this.unsubscribe?.();
    this.setStatus(this.assignmentValue ? this.statusValue : "ready");
    this.unsubscribe = this.transport.subscribe(
      credential.nodeId,
      credential.token,
      this.latestSequence,
      (assignment) => { void this.receive(assignment); },
      (error) => { void this.transportFailed(error); },
    );
  }

  private async receive(assignment: Assignment): Promise<void> {
    const credential = this.credentialValue;
    if (!credential || assignment.nodeId !== credential.nodeId || assignment.sequence <= this.latestSequence) return;
    this.latestSequence = assignment.sequence;
    this.assignmentValue = assignment;
    const operation = ++this.operation;
    this.errorValue = null;
    if (!isSupportedMedia(assignment.mediaType, assignment.mediaUrl)) {
      this.media.clear();
      await this.report("unsupported", 0, assignment, credential);
      if (operation === this.operation) this.setStatus("unsupported");
      return;
    }
    try {
      if (isVideo(assignment)) {
        await this.media.loadVideo(assignment.mediaUrl);
        await this.media.playVideo();
      } else {
        // showImage resolves only after the image load event, so the report is
        // an observation rather than a declaration that a URL was accepted.
        await this.media.showImage(assignment.mediaUrl);
      }
      if (operation === this.operation) {
        await this.report("playing", 0, assignment, credential);
        this.setStatus("playing");
      }
    } catch (error) {
      if (operation !== this.operation) return;
      if (isVideo(assignment)) {
        await this.report("autoplay_suspended", 0, assignment, credential);
        this.setStatus("autoplay_suspended");
      } else {
        await this.report("offline", 0, assignment, credential);
        this.setStatus("offline");
      }
      this.errorValue = error;
      this.notify();
    }
  }

  private async transportFailed(error: unknown): Promise<void> {
    this.errorValue = error;
    const assignment = this.assignmentValue;
    const credential = this.credentialValue;
    if (assignment && credential) await this.report("offline", 0, assignment, credential);
    this.setStatus("offline");
  }

  private async report(state: PlaybackState, positionMs: number, assignment: Assignment, credential: NodeCredential): Promise<void> {
    const report: PlaybackReport = {
      assignmentId: assignment.id,
      eventEpoch: assignment.eventEpoch,
      sequence: assignment.sequence,
      state,
      positionMs,
      observedAt: new Date().toISOString(),
    };
    await this.transport.acknowledge(credential.nodeId, credential.token, report);
  }

  private requireCredential(): NodeCredential {
    if (!this.credentialValue) throw new Error("browser node is not claimed");
    return this.credentialValue;
  }
  private setError(error: unknown): void { this.errorValue = error; this.setStatus("error"); }
  private setStatus(status: BrowserNodeStatus): void { this.statusValue = status; this.notify(); }
  private notify(): void { const snapshot = this.snapshot; for (const listener of this.listeners) listener(snapshot); }
}
