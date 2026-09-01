import { describe, expect, it } from "vitest";
import { BrowserNodeController } from "./controller";
import { MemoryCredentialStorage } from "./storage";
import type { Assignment, BrowserNodeTransport, MediaPort, NodeCredential, PlaybackReport } from "./types";

class FakeTransport implements BrowserNodeTransport {
  readonly reports: PlaybackReport[] = [];
  private listener: ((assignment: Assignment) => void) | null = null;
  private error: ((error: unknown) => void) | null = null;
  subscriptions = 0;
  async claim(nodeId: string, _code: string): Promise<NodeCredential> { return { nodeId, token: "scoped-token", expiresAt: "2999-01-01T00:00:00.000Z" }; }
  async renew(nodeId: string, _token: string): Promise<NodeCredential> { return { nodeId, token: "renewed-token", expiresAt: "2999-01-01T00:00:00.000Z" }; }
  subscribe(_nodeId: string, _token: string, _after: number, onAssignment: (assignment: Assignment) => void, onError: (error: unknown) => void): () => void {
    this.listener = onAssignment; this.error = onError; this.subscriptions += 1;
    return () => { this.listener = null; this.error = null; };
  }
  async acknowledge(_nodeId: string, _token: string, report: PlaybackReport): Promise<void> { this.reports.push(report); }
  emit(assignment: Assignment): void { this.listener?.(assignment); }
  fail(error: unknown): void { this.error?.(error); }
}

class FakeMedia implements MediaPort {
  imageLoads = 0;
  videoLoads = 0;
  playAttempts = 0;
  playResult: Promise<void> = Promise.resolve();
  async showImage(_url: string): Promise<void> { this.imageLoads += 1; }
  async loadVideo(_url: string): Promise<void> { this.videoLoads += 1; }
  async playVideo(): Promise<void> { this.playAttempts += 1; return this.playResult; }
  clear(): void {}
}

const image: Assignment = { id: "a-1", nodeId: "node-a", sequence: 1, eventEpoch: "epoch", mediaUrl: "https://cdn.test/one.png", mediaType: "image/png" };
const video: Assignment = { id: "a-2", nodeId: "node-a", sequence: 2, eventEpoch: "epoch", mediaUrl: "https://cdn.test/two.mp4", mediaType: "video/mp4" };

async function settle(): Promise<void> {
  for (let index = 0; index < 8; index += 1) await Promise.resolve();
}

describe("BrowserNodeController", () => {
  it("claims a node, plays a supported image, and acknowledges observed playback", async () => {
    const transport = new FakeTransport(); const media = new FakeMedia();
    const controller = new BrowserNodeController(transport, new MemoryCredentialStorage(), media);
    await controller.claim("node-a", "ABCD-1234");
    transport.emit(image); await settle();
    expect(media.imageLoads).toBe(1);
    expect(controller.snapshot.status).toBe("playing");
    expect(transport.reports).toMatchObject([{ assignmentId: "a-1", state: "playing", sequence: 1 }]);
  });

  it("surfaces autoplay denial and recovers on a user gesture", async () => {
    const transport = new FakeTransport(); const media = new FakeMedia();
    media.playResult = Promise.reject(new DOMException("gesture required", "NotAllowedError"));
    const controller = new BrowserNodeController(transport, new MemoryCredentialStorage(), media);
    await controller.claim("node-a", "ABCD-1234"); transport.emit(video); await settle();
    expect(controller.snapshot.status).toBe("autoplay_suspended");
    expect(transport.reports.at(-1)).toMatchObject({ assignmentId: "a-2", state: "autoplay_suspended" });
    media.playResult = Promise.resolve(); await controller.resumeAutoplay();
    expect(controller.snapshot.status).toBe("playing");
    expect(transport.reports.at(-1)).toMatchObject({ assignmentId: "a-2", state: "playing" });
  });

  it("reloads from the scoped credential without persisting assignments", async () => {
    const transport = new FakeTransport(); const storage = new MemoryCredentialStorage();
    const first = new BrowserNodeController(transport, storage, new FakeMedia());
    await first.claim("node-a", "ABCD-1234"); transport.emit(image); await settle();
    const second = new BrowserNodeController(transport, storage, new FakeMedia());
    await second.start();
    expect(second.snapshot.credential?.token).toBe("scoped-token");
    expect(second.snapshot.assignment).toBeNull();
    expect(transport.subscriptions).toBe(2);
  });
});
