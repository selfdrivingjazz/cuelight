import { ApiError, BrowserNodeApi } from "./api";
import { toPresentation, withAppToken } from "./presentation";
import type { Presentation } from "./presentation";
import type { CredentialStorage } from "./storage";
import type { Assignment, BrowserCredential, ClaimStarted, ContentState, RemoteEvent, RemoteSession, SoftwareTarget } from "./types";

export type NodeView =
  | { phase: "starting" }
  | { phase: "pairing"; claim: ClaimStarted }
  | { phase: "idle"; nodeId: string }
  | { phase: "presenting"; nodeId: string; assignment: Assignment; presentation: Presentation };

export type NodeSnapshot = {
  view: NodeView;
  /** True while the control plane is unreachable; the last presentation keeps playing. */
  offline: boolean;
  remote: RemoteSession | null;
};

/** What the renderer observed; reported to the control plane as currentContent. */
export type RenderState = { state: ContentState; itemId?: string; positionMs?: number; width?: number; height?: number; error?: string };

/** The Android shell's bridge (apps/node-android); update calls are absent in older shells. */
export type AndroidBridge = { appVersion?: () => string; installUpdate?: (json: string) => boolean };

export type ControllerOptions = {
  userAgent?: string;
  sleep?: (ms: number, signal: AbortSignal) => Promise<void>;
  now?: () => number;
  assignmentWaitSeconds?: number;
  /** The version this website-node build was made from. */
  buildVersion?: string;
  /** Defaults to window.CuelightAndroid. */
  android?: AndroidBridge;
  /** Reloads the page to pick up the website-node build control now serves. */
  reload?: () => void;
  /** Settling time between seeing a new control version and reloading. */
  reloadDelayMs?: number;
};

const RENDERER_VERSION = "website-node/0.2.0";
const RELOADED_FOR_KEY = "cuelight-reloaded-for-version";

/** Pairing, assignment, report, and remote-input loops for one browser node. */
export class BrowserNodeController {
  private snapshotValue: NodeSnapshot = { view: { phase: "starting" }, offline: false, remote: null };
  private readonly listeners = new Set<(snapshot: NodeSnapshot) => void>();
  private readonly remoteListeners = new Set<(event: RemoteEvent) => void>();
  private abort = new AbortController();
  private remoteAbort: AbortController | null = null;
  private credential: BrowserCredential | null = null;
  private reportSequence: number;
  private readonly sleep: (ms: number, signal: AbortSignal) => Promise<void>;
  private readonly now: () => number;
  private readonly waitSeconds: number;
  private readonly buildVersion: string;
  private readonly android: AndroidBridge | undefined;
  private reloadTimer: ReturnType<typeof setTimeout> | undefined;
  private installRequested = "";

  constructor(private readonly api: BrowserNodeApi, private readonly storage: CredentialStorage, private readonly options: ControllerOptions = {}) {
    this.sleep = options.sleep || abortableSleep;
    this.now = options.now || Date.now;
    this.waitSeconds = options.assignmentWaitSeconds === undefined ? 25 : options.assignmentWaitSeconds;
    // Millisecond clock seeds a sequence that stays monotonic across reloads.
    this.reportSequence = this.now();
    this.buildVersion = options.buildVersion || __CUELIGHT_VERSION__;
    this.android = options.android || (typeof window === "undefined" ? undefined : window.CuelightAndroid);
    api.onServerVersion = (version) => this.serverVersion(version);
  }

  get snapshot(): NodeSnapshot { return this.snapshotValue; }

  subscribe(listener: (snapshot: NodeSnapshot) => void): () => void {
    this.listeners.add(listener);
    return () => { this.listeners.delete(listener); };
  }

  onRemoteEvent(listener: (event: RemoteEvent) => void): () => void {
    this.remoteListeners.add(listener);
    return () => { this.remoteListeners.delete(listener); };
  }

  start(): void {
    this.stop();
    this.abort = new AbortController();
    void this.main(this.abort.signal);
  }

  stop(): void {
    this.abort.abort();
    this.stopRemote();
    clearTimeout(this.reloadTimer);
    this.reloadTimer = undefined;
  }

  /** The software this node reports: the Android shell's version when it can update itself. */
  private software(): { softwareVersion: string; updateMethod: "browser" | "android" } {
    const android = this.android;
    if (android && android.appVersion && android.installUpdate) return { softwareVersion: android.appVersion(), updateMethod: "android" };
    return { softwareVersion: this.buildVersion, updateMethod: "browser" };
  }

  /**
   * Control serves the website-node build matching its own version, so a
   * different X-Cuelight-Version means a newer page is available. Reload once
   * per control version (a page that still differs after reloading stays put)
   * and never while a pairing code is on screen.
   */
  private serverVersion(version: string): void {
    if (version === this.buildVersion || this.reloadTimer !== undefined || readSession(RELOADED_FOR_KEY) === version) return;
    const delay = this.options.reloadDelayMs === undefined ? 10000 : this.options.reloadDelayMs;
    const attempt = () => {
      if (this.snapshotValue.view.phase === "pairing") {
        this.reloadTimer = setTimeout(attempt, delay);
        return;
      }
      this.reloadTimer = undefined;
      writeSession(RELOADED_FOR_KEY, version);
      (this.options.reload || (() => window.location.reload()))();
    };
    this.reloadTimer = setTimeout(attempt, delay);
  }

  /** Hands a newer signed APK to the Android shell, once per version per page load. */
  private installSoftware(target: SoftwareTarget | undefined): void {
    const android = this.android;
    if (!target || !target.apkUrl || !target.apkSha256 || !android || !android.installUpdate) return;
    const reported = this.software().softwareVersion;
    if (this.installRequested === target.version || compareVersions(target.version, reported) <= 0) return;
    this.installRequested = target.version;
    const apkUrl = typeof location === "undefined" ? target.apkUrl : new URL(target.apkUrl, location.href).toString();
    android.installUpdate(JSON.stringify({ version: target.version, apkUrl, apkSha256: target.apkSha256 }));
  }

  /** Forget the credential locally and show a fresh pairing code. */
  unpair(): void {
    this.storage.clear();
    this.credential = null;
    this.start();
  }

  /** Called by the renderer whenever playback state changes. */
  async reportRender(render: RenderState): Promise<void> {
    const credential = this.credential;
    const view = this.snapshotValue.view;
    if (!credential || view.phase !== "presenting") return;
    const { assignment, presentation } = view;
    const rendering = presentation.kind !== "unsupported" && !render.error;
    const presentationKind = presentation.kind === "unsupported" ? presentation.presentationKind : presentation.kind;
    const digest = rendering ? presentation.releaseDigest : null;
    this.reportSequence = Math.max(this.reportSequence + 1, this.now());
    const reportedAt = new Date(this.now()).toISOString();
    try {
      await this.api.report(credential.token, {
        eventEpoch: credential.eventEpoch,
        sequence: this.reportSequence,
        desiredGenerationSeen: assignment.desiredGeneration,
        observedGeneration: rendering ? assignment.desiredGeneration : 0,
        presentationKind,
        renderingReleaseDigest: digest,
        candidateReleaseDigest: presentation.releaseDigest,
        healthyReleaseDigest: digest,
        lastKnownGoodReleaseDigest: digest,
        currentContent: { releaseDigest: presentation.releaseDigest, presentationKind, state: render.state, transitionedAt: reportedAt, itemId: render.itemId, positionMs: render.positionMs, width: render.width, height: render.height },
        reportedAt,
        conditions: [rendering
          ? { type: "Rendering", status: true, reason: "BrowserPlayback" }
          : { type: "Rendering", status: false, reason: presentation.kind === "unsupported" ? "UnsupportedPresentation" : "PlaybackFailed", message: presentation.kind === "unsupported" ? presentation.reason : render.error }],
      });
    } catch (error) {
      if (isUnauthorized(error)) this.unpair();
    }
  }

  private async main(signal: AbortSignal): Promise<void> {
    while (!signal.aborted) {
      this.credential = this.storage.load();
      if (!this.credential) {
        const credential = await this.pair(signal);
        if (!credential) return;
        this.storage.save(credential);
        this.credential = credential;
      }
      await this.serve(signal);
    }
  }

  private async pair(signal: AbortSignal): Promise<BrowserCredential | null> {
    let failures = 0;
    while (!signal.aborted) {
      try {
        const claim = await this.api.startClaim(this.options.userAgent || "");
        failures = 0;
        this.update({ view: { phase: "pairing", claim }, offline: false, remote: null });
        const credential = await this.awaitClaim(claim, signal);
        if (credential) return credential;
      } catch {
        if (signal.aborted) return null;
        failures += 1;
        this.update({ offline: true });
        await this.sleep(backoff(failures), signal).catch(() => undefined);
      }
    }
    return null;
  }

  /** Polls one claim until it is redeemed (credential) or expires (null). */
  private async awaitClaim(claim: ClaimStarted, signal: AbortSignal): Promise<BrowserCredential | null> {
    const interval = Math.max(1, claim.pollIntervalSeconds) * 1000;
    while (!signal.aborted && this.now() < Date.parse(claim.expiresAt)) {
      await this.sleep(interval, signal).catch(() => undefined);
      if (signal.aborted) return null;
      try {
        const status = await this.api.pollClaim(claim.claimId, claim.pollToken);
        this.update({ offline: false });
        if (status.status === "claimed") return status.credential;
      } catch (error) {
        if (error instanceof ApiError && (error.status === 410 || error.status === 401)) return null;
        this.update({ offline: true });
      }
    }
    return null;
  }

  /** Runs the assignment loop until the credential is rejected or the controller stops. */
  private async serve(signal: AbortSignal): Promise<void> {
    const credential = this.credential;
    if (!credential) return;
    if (this.snapshotValue.view.phase === "starting" || this.snapshotValue.view.phase === "pairing") {
      this.update({ view: { phase: "idle", nodeId: credential.nodeId } });
    }
    let etag = "";
    let failures = 0;
    let capabilitiesSent = false;
    while (!signal.aborted && this.credential) {
      try {
        await this.renewIfDue();
        const token = this.credential.token;
        if (!capabilitiesSent) {
          await this.api.putCapabilities(token, { ...capabilities(), ...this.software() });
          capabilitiesSent = true;
        }
        const poll = await this.api.pollAssignment(token, etag, this.waitSeconds, signal);
        failures = 0;
        if (this.snapshotValue.offline) this.update({ offline: false });
        if ("unassigned" in poll) {
          etag = "";
          this.stopRemote();
          if (this.snapshotValue.view.phase !== "idle") this.update({ view: { phase: "idle", nodeId: credential.nodeId }, remote: null });
          await this.sleep(this.waitSeconds * 1000, signal).catch(() => undefined);
          continue;
        }
        if ("notModified" in poll) continue;
        this.installSoftware(poll.assignment.software);
        if (!poll.assignment.releaseId) {
          // Software-only: nothing to present, but keep long-polling on its ETag.
          etag = poll.etag;
          this.stopRemote();
          if (this.snapshotValue.view.phase !== "idle") this.update({ view: { phase: "idle", nodeId: credential.nodeId }, remote: null });
          continue;
        }
        const release = await this.api.release(token, poll.assignment.releaseId);
        let presentation = toPresentation(release, (digest) => this.api.artifactUrl(token, digest));
        if (presentation.kind === "web" && presentation.appAccess && presentation.mediaId) {
          try {
            const app = await this.api.appToken(token, presentation.mediaId);
            presentation = { ...presentation, url: withAppToken(presentation.url, app.token, app.apiUrl) };
          } catch (error) {
            // Network failures and a rejected credential go through the poll loop's handling; a refused grant fails the presentation.
            if (!(error instanceof ApiError) || isUnauthorized(error)) throw error;
            presentation = { kind: "unsupported", releaseDigest: presentation.releaseDigest, presentationKind: "web", reason: `App token unavailable: ${error.message}` };
          }
        }
        etag = poll.etag;
        this.update({ view: { phase: "presenting", nodeId: credential.nodeId, assignment: poll.assignment, presentation } });
        this.syncRemote(poll.assignment, presentation.kind === "web" && presentation.appAccess);
        if (presentation.kind === "unsupported") void this.reportRender({ state: "off_air", error: presentation.reason });
      } catch (error) {
        if (signal.aborted) return;
        if (isUnauthorized(error)) {
          this.storage.clear();
          this.credential = null;
          this.stopRemote();
          this.update({ view: { phase: "starting" }, remote: null, offline: false });
          return;
        }
        failures += 1;
        this.update({ offline: true });
        await this.sleep(backoff(failures), signal).catch(() => undefined);
      }
    }
  }

  private async renewIfDue(): Promise<void> {
    const credential = this.credential;
    if (!credential || this.now() < Date.parse(credential.renewAfter)) return;
    const renewed = await this.api.renew(credential.token);
    this.storage.save(renewed);
    this.credential = renewed;
  }

  /** Opens the remote session; an app access page reads its events itself, so the node only keeps it alive. */
  private syncRemote(assignment: Assignment, appAccess: boolean): void {
    this.stopRemote();
    if (!assignment.remoteInput || !this.credential) {
      if (this.snapshotValue.remote) this.update({ remote: null });
      return;
    }
    const abort = new AbortController();
    this.remoteAbort = abort;
    void this.remoteLoop(assignment.desiredGeneration, appAccess, abort.signal);
  }

  private stopRemote(): void {
    if (this.remoteAbort) this.remoteAbort.abort();
    this.remoteAbort = null;
  }

  private async remoteLoop(generation: number, appAccess: boolean, signal: AbortSignal): Promise<void> {
    let after = 0;
    let failures = 0;
    let session: RemoteSession | null = null;
    while (!signal.aborted && this.credential) {
      try {
        if (!session || this.now() >= Date.parse(session.expiresAt)) {
          session = await this.api.createRemoteSession(this.credential.token, generation);
          if (signal.aborted) return;
          this.update({ remote: session });
        }
        if (appAccess) {
          await this.sleep(Math.max(1000, Date.parse(session.expiresAt) - this.now()), signal).catch(() => undefined);
          continue;
        }
        const page = await this.api.remoteEvents(this.credential.token, session.sessionId, after, 25, signal);
        failures = 0;
        after = page.nextSequence;
        for (const event of page.events) for (const listener of this.remoteListeners) listener(event);
      } catch (error) {
        if (signal.aborted) return;
        if (error instanceof ApiError && (error.status === 404 || error.status === 410)) session = null;
        failures += 1;
        await this.sleep(backoff(failures), signal).catch(() => undefined);
      }
    }
  }

  private update(patch: Partial<NodeSnapshot>): void {
    this.snapshotValue = { ...this.snapshotValue, ...patch };
    for (const listener of this.listeners) listener(this.snapshotValue);
  }
}

function isUnauthorized(error: unknown): boolean {
  return error instanceof ApiError && error.status === 401;
}

function backoff(failures: number): number {
  return Math.min(30000, 1000 * Math.pow(2, Math.min(failures, 5)));
}

/** Semantic-version precedence of MAJOR.MINOR.PATCH[-PRERELEASE]; unparseable versions sort lowest. */
function compareVersions(a: string, b: string): number {
  const pattern = /^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$/;
  const left = pattern.exec(a);
  const right = pattern.exec(b);
  if (!left || !right) return left ? 1 : right ? -1 : 0;
  for (let index = 1; index <= 3; index += 1) {
    const difference = Number(left[index]) - Number(right[index]);
    if (difference !== 0) return difference;
  }
  const leftPre = left[4] || "";
  const rightPre = right[4] || "";
  if (leftPre === rightPre) return 0;
  if (!leftPre || !rightPre) return leftPre ? -1 : 1;
  const leftParts = leftPre.split(".");
  const rightParts = rightPre.split(".");
  for (let index = 0; index < Math.min(leftParts.length, rightParts.length); index += 1) {
    const x = leftParts[index] || "";
    const y = rightParts[index] || "";
    if (x === y) continue;
    const xNumeric = /^\d+$/.test(x);
    const yNumeric = /^\d+$/.test(y);
    if (xNumeric && yNumeric) return Number(x) - Number(y);
    if (xNumeric !== yNumeric) return xNumeric ? -1 : 1;
    return x < y ? -1 : 1;
  }
  return leftParts.length - rightParts.length;
}

/** sessionStorage survives the reload it guards; private modes and old TV browsers may lack it. */
function readSession(key: string): string | null {
  try { return typeof sessionStorage === "undefined" ? null : sessionStorage.getItem(key); } catch { return null; }
}

function writeSession(key: string, value: string): void {
  try { if (typeof sessionStorage !== "undefined") sessionStorage.setItem(key, value); } catch { /* reload loops are then only bounded by the delay */ }
}

function abortableSleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) { reject(new Error("aborted")); return; }
    const timer = setTimeout(() => { signal.removeEventListener("abort", onAbort); resolve(); }, ms);
    const onAbort = () => { clearTimeout(timer); reject(new Error("aborted")); };
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

function capabilities() {
  const video = typeof document === "undefined" ? null : document.createElement("video");
  const accepted = ["video/mp4", "video/webm", "image/png", "image/jpeg", "image/gif", "image/webp"].filter((type) =>
    type.indexOf("image/") === 0 || (video !== null && video.canPlayType(type) !== ""));
  return {
    platform: "browser",
    architecture: typeof navigator === "undefined" ? "unknown" : (navigator.platform || "unknown"),
    protocolVersion: "1.0",
    rendererVersion: RENDERER_VERSION,
    profileId: "browser",
    features: ["media", "media-schedule", "web"],
    acceptedMediaTypes: accepted,
    outputReady: true,
    clockReliable: true,
  };
}
