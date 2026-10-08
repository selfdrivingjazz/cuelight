import { describe, expect, it } from "vitest";
import { BrowserNodeApi } from "./api";
import { BrowserNodeController } from "./controller";
import type { NodeSnapshot } from "./controller";
import { isWebItem, nextScheduleChange, scheduledItem, toPresentation } from "./presentation";
import type { MediaItem } from "./presentation";
import { MemoryCredentialStorage } from "./storage";
import type { BrowserCredential, ReleaseEnvelope } from "./types";

type Route = (init: RequestInit, url: string) => Response | Promise<Response>;

const DIGEST = "a".repeat(64);
const release: ReleaseEnvelope = {
  id: "release-1",
  digest: "d".repeat(64),
  payload: {
    presentation: { kind: "media", media: { playback: { fit: "fit", loop: true, muted: true, volume: 0, orientation: 0 } } },
    artifacts: [{ digest: DIGEST, size: 10, mediaType: "video/mp4" }],
  },
};
const assignment = { id: "as-1", nodeId: "node-1", releaseId: "release-1", releaseDigest: release.digest, desiredGeneration: 3 };

function json(status: number, body?: unknown, headers: Record<string, string> = {}): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { "content-type": "application/json", ...headers } });
}

function hang(init: RequestInit): Promise<Response> {
  return new Promise((_, reject) => init.signal && init.signal.addEventListener("abort", () => reject(new Error("aborted"))));
}

function harness(routes: Record<string, Route>, now = () => Date.parse("2026-10-03T12:00:00Z"), sleep: (ms: number, signal: AbortSignal) => Promise<void> = async () => undefined) {
  const calls: { key: string; auth: string; url: string }[] = [];
  const fetcher = async (url: string, init: RequestInit = {}) => {
    const path = url.replace("/api/v1/browser-node", "").split("?")[0] || "";
    const key = `${init.method} ${path}`;
    const headers = (init.headers || {}) as Record<string, string>;
    calls.push({ key, auth: headers.authorization || "", url });
    const route = routes[key];
    if (!route) return json(500, { detail: `unexpected ${key}` });
    return route(init, url);
  };
  const storage = new MemoryCredentialStorage();
  const controller = new BrowserNodeController(new BrowserNodeApi("", fetcher), storage, { now, sleep, assignmentWaitSeconds: 1, controlOrigin: "https://control.example" });
  return { controller, storage, calls };
}

/** Resolves once the controller publishes a snapshot satisfying predicate. */
function waitFor(controller: BrowserNodeController, predicate: (snapshot: NodeSnapshot) => boolean): Promise<void> {
  return new Promise((resolve) => {
    if (predicate(controller.snapshot)) { resolve(); return; }
    const unsubscribe = controller.subscribe((snapshot) => { if (predicate(snapshot)) { unsubscribe(); resolve(); } });
  });
}

const credential: BrowserCredential = { nodeId: "node-1", token: "tok-1", expiresAt: "2026-11-01T00:00:00Z", renewAfter: "2026-10-15T00:00:00Z", eventEpoch: "epoch" };
const claim = { claimId: "claim-1", code: "K7QM4XPD", displayCode: "K7QM-4XPD", pollToken: "poll-1", expiresAt: "2026-10-03T12:10:00Z", pollIntervalSeconds: 1, pairUrl: "https://console/pair?code=K7QM4XPD" };

describe("BrowserNodeController", () => {
  it("pairs, stores the credential, and presents the assigned media with a tokenized artifact URL", async () => {
    let polls = 0;
    let assignmentCalls = 0;
    const { controller, storage, calls } = harness({
      "POST /claims": () => json(201, claim),
      "GET /claims/claim-1": () => (++polls < 2 ? json(200, { status: "pending" }) : json(200, { status: "claimed", credential })),
      "PUT /player/capabilities": () => json(204),
      "GET /player/assignment": (init) => (++assignmentCalls === 1 ? json(200, assignment, { etag: "\"assignment-3\"" }) : hang(init)),
      "GET /player/releases/release-1": () => json(200, release),
    });
    const phases: string[] = [];
    controller.subscribe((snapshot) => { phases.push(snapshot.view.phase); });
    const presenting = waitFor(controller, (snapshot) => snapshot.view.phase === "presenting");
    controller.start();
    await presenting;
    controller.stop();

    expect(phases).toContain("pairing");
    expect(storage.load()).toEqual(credential);
    expect(calls.find((call) => call.key === "GET /claims/claim-1")!.auth).toBe("Bearer poll-1");
    expect(calls.find((call) => call.key === "GET /player/assignment")!.auth).toBe("Bearer tok-1");
    const view = controller.snapshot.view;
    if (view.phase !== "presenting" || view.presentation.kind !== "media") throw new Error("not presenting media");
    expect(view.presentation.item.url).toBe(`/api/v1/browser-node/player/artifacts/${DIGEST}/content?access_token=tok-1`);
    expect(view.presentation.item.element).toBe("video");
  });

  it("drops a revoked credential and returns to pairing with a fresh claim", async () => {
    let claims = 0;
    const { controller, storage } = harness({
      "POST /claims": (init) => (++claims === 1 ? json(201, claim) : hang(init)),
      "GET /claims/claim-1": (init) => hang(init),
      "PUT /player/capabilities": () => json(204),
      "GET /player/assignment": () => json(401, { detail: "browser node credential is not valid" }),
    });
    storage.save(credential);
    const pairing = waitFor(controller, (snapshot) => snapshot.view.phase === "pairing");
    controller.start();
    await pairing;
    controller.stop();
    expect(storage.load()).toBeNull();
  });

  it("renews a credential past renewAfter and uses the new token", async () => {
    const renewed = { ...credential, token: "tok-2", renewAfter: "2026-12-01T00:00:00Z" };
    let polled = () => undefined as void;
    const assignmentPolled = new Promise<void>((resolve) => { polled = resolve; });
    const { controller, storage, calls } = harness({
      "POST /credentials:renew": () => json(200, renewed),
      "PUT /player/capabilities": () => json(204),
      "GET /player/assignment": (init) => { polled(); return hang(init); },
    }, () => Date.parse("2026-10-20T00:00:00Z"));
    storage.save(credential);
    controller.start();
    await assignmentPolled;
    controller.stop();
    expect(calls[0]).toMatchObject({ key: "POST /credentials:renew", auth: "Bearer tok-1" });
    expect(calls.find((call) => call.key === "GET /player/assignment")!.auth).toBe("Bearer tok-2");
    expect(storage.load()!.token).toBe("tok-2");
  });

  const webRelease = (url: string): ReleaseEnvelope => ({ ...release, payload: { presentation: { kind: "web", web: { url } }, artifacts: [] } });
  /** Presents a web release with remote input on and stops once the remote loop parks (event poll or guide keep-alive sleep). */
  const presentWeb = async (url: string) => {
    let assignmentCalls = 0;
    let parked = () => undefined as void;
    const remoteParked = new Promise<void>((resolve) => { parked = resolve; });
    const { controller, storage, calls } = harness({
      "PUT /player/capabilities": () => json(204),
      "GET /player/assignment": (init) => (++assignmentCalls === 1 ? json(200, { ...assignment, remoteInput: true }, { etag: "\"a\"" }) : hang(init)),
      "GET /player/releases/release-1": () => json(200, webRelease(url)),
      "POST /player/cable-guide-tokens": () => json(201, { token: "guide/1", expiresAt: "2026-10-04T12:00:00Z" }),
      "POST /player/remote-sessions": () => json(201, { sessionId: "rs-1", remoteUrl: "https://control.example/remote/rs-1", showQr: true, expiresAt: "2026-10-03T12:10:00Z" }),
      "GET /player/remote-sessions/rs-1/events": (init) => { parked(); return hang(init); },
    }, undefined, (_ms, signal) => { parked(); return new Promise((resolve) => signal.addEventListener("abort", () => resolve())); });
    storage.save(credential);
    controller.start();
    await remoteParked;
    controller.stop();
    const view = controller.snapshot.view;
    if (view.phase !== "presenting" || view.presentation.kind !== "web") throw new Error("not presenting web");
    return { presentation: view.presentation, keys: calls.map((call) => call.key), calls };
  };

  it("mints a guide token for the control-hosted Cable guide and leaves its remote events to the guide", async () => {
    const { presentation, keys, calls } = await presentWeb("https://control.example/cable/?deployment=dep-1#stale");
    expect(presentation).toMatchObject({ guide: true, url: "https://control.example/cable/?deployment=dep-1#token=guide%2F1" });
    expect(calls.find((call) => call.key === "POST /player/cable-guide-tokens")!.auth).toBe("Bearer tok-1");
    expect(keys).toContain("POST /player/remote-sessions");
    expect(keys).not.toContain("GET /player/remote-sessions/rs-1/events");
  });

  it("shows other web pages unchanged and forwards their remote events", async () => {
    for (const url of ["https://other.example/cable/?deployment=dep-1", "https://control.example/cable/", "https://control.example/cable/x?deployment=dep-1"]) {
      const { presentation, keys } = await presentWeb(url);
      expect(presentation).toMatchObject({ guide: false, url });
      expect(keys).not.toContain("POST /player/cable-guide-tokens");
      expect(keys).toContain("GET /player/remote-sessions/rs-1/events");
    }
  });
});

describe("presentation mapping", () => {
  it("keeps web URLs and maps schedule web items alongside media items", () => {
    const web = toPresentation({ ...release, payload: { ...release.payload, presentation: { kind: "web", web: { url: "https://example.com/" } } } }, (digest) => digest);
    expect(web).toMatchObject({ kind: "web", url: "https://example.com/", guide: false });
    const playback = { fit: "fit" as const, loop: false, muted: true, volume: 0, orientation: 0 };
    const schedule = toPresentation({ ...release, payload: { ...release.payload, presentation: { kind: "media-schedule", mediaSchedule: { timezone: "UTC", items: [
      { ...playback, id: "page", startsAt: "2026-10-03T12:01:00Z", endsAt: "2026-10-03T12:02:00Z", web: { url: "https://example.com/page" } },
      { ...playback, id: "clip", startsAt: "2026-10-03T12:00:00Z", endsAt: "2026-10-03T12:01:00Z", artifactDigest: DIGEST, mediaType: "video/mp4" },
    ] } } } }, (digest) => `/media/${digest}`);
    if (schedule.kind !== "media-schedule") throw new Error("not a schedule");
    expect(schedule.items.map((item) => (isWebItem(item) ? `web ${item.webUrl}` : `media ${item.url}`))).toEqual([`media /media/${DIGEST}`, "web https://example.com/page"]);
  });

  it("selects the airing schedule item with start-inclusive, end-exclusive bounds", () => {
    const item = (id: string, startsAt: number, endsAt: number): MediaItem => ({ id, url: id, mediaType: "image/png", element: "image", fit: "fit", loop: false, muted: true, volume: 0, orientation: 0, startsAt, endsAt });
    const items = [item("a", 100, 200), item("b", 200, 300)];
    expect(scheduledItem(items, 99)).toBeNull();
    expect(scheduledItem(items, 100)!.id).toBe("a");
    expect(scheduledItem(items, 200)!.id).toBe("b");
    expect(scheduledItem(items, 300)).toBeNull();
    expect(nextScheduleChange(items, 150)).toBe(50);
    expect(nextScheduleChange(items, 300)).toBeNull();
  });
});
