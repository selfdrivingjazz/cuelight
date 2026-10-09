import { describe, expect, it, vi } from "vitest";
import { BrowserNodeApi } from "./api";
import { BrowserNodeController } from "./controller";
import type { ControllerOptions, NodeSnapshot } from "./controller";
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

function harness(routes: Record<string, Route>, now = () => Date.parse("2026-10-03T12:00:00Z"), sleep: (ms: number, signal: AbortSignal) => Promise<void> = async () => undefined, options: ControllerOptions = {}) {
  const calls: { key: string; auth: string; url: string; body?: unknown }[] = [];
  const fetcher = async (url: string, init: RequestInit = {}) => {
    const path = url.replace("/api/v1/browser-node", "").split("?")[0] || "";
    const key = `${init.method} ${path}`;
    const headers = (init.headers || {}) as Record<string, string>;
    calls.push({ key, auth: headers.authorization || "", url, body: init.body });
    const route = routes[key];
    if (!route) return json(500, { detail: `unexpected ${key}` });
    return route(init, url);
  };
  const storage = new MemoryCredentialStorage();
  const controller = new BrowserNodeController(new BrowserNodeApi("", fetcher), storage, { now, sleep, assignmentWaitSeconds: 1, buildVersion: "0.2.0", ...options });
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

  const webRelease = (web: { url: string; appAccess?: boolean; mediaId?: string }): ReleaseEnvelope => ({ ...release, payload: { presentation: { kind: "web", web }, artifacts: [] } });
  /** Presents a web release with remote input on and stops once the remote loop parks (event poll or app keep-alive sleep). */
  const presentWeb = async (web: { url: string; appAccess?: boolean; mediaId?: string }) => {
    let assignmentCalls = 0;
    let parked = () => undefined as void;
    const remoteParked = new Promise<void>((resolve) => { parked = resolve; });
    const { controller, storage, calls } = harness({
      "PUT /player/capabilities": () => json(204),
      "GET /player/assignment": (init) => (++assignmentCalls === 1 ? json(200, { ...assignment, remoteInput: true }, { etag: "\"a\"" }) : hang(init)),
      "GET /player/releases/release-1": () => json(200, webRelease(web)),
      "POST /player/app-tokens": () => json(201, { token: "app/1", expiresAt: "2026-10-04T12:00:00Z", apiUrl: "https://control.example/api/v1" }),
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

  const fragment = "cuelight-token=app%2F1&cuelight-api=https%3A%2F%2Fcontrol.example%2Fapi%2Fv1";

  it("mints an app token for an app access page and leaves its remote events to the page", async () => {
    const { presentation, keys, calls } = await presentWeb({ url: "https://app.example/x", appAccess: true, mediaId: "media-7" });
    expect(presentation).toMatchObject({ appAccess: true, url: `https://app.example/x#${fragment}` });
    const mint = calls.find((call) => call.key === "POST /player/app-tokens")!;
    expect(mint.auth).toBe("Bearer tok-1");
    expect(JSON.parse(String(mint.body))).toEqual({ mediaId: "media-7" });
    expect(keys).toContain("POST /player/remote-sessions");
    expect(keys).not.toContain("GET /player/remote-sessions/rs-1/events");
  });

  it("keeps existing fragment params and replaces stale app token params", async () => {
    const { presentation } = await presentWeb({ url: "https://app.example/x?q=1#view=grid&cuelight-token=old&cuelight-api=old", appAccess: true, mediaId: "media-7" });
    expect(presentation.url).toBe(`https://app.example/x?q=1#view=grid&${fragment}`);
  });

  it("shows other web pages unchanged without minting and forwards their remote events", async () => {
    const url = "https://other.example/page#view=grid";
    const { presentation, keys } = await presentWeb({ url });
    expect(presentation).toMatchObject({ appAccess: false, url });
    expect(keys).not.toContain("POST /player/app-tokens");
    expect(keys).toContain("GET /player/remote-sessions/rs-1/events");
  });

  it("defers the reload for a new control version while pairing, and reloads once per version", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const session = new Map<string, string>();
    vi.stubGlobal("sessionStorage", { getItem: (key: string) => session.get(key) ?? null, setItem: (key: string, value: string) => { session.set(key, value); } });
    try {
      const reloads: string[] = [];
      const sleeps: (() => void)[] = [];
      const sleep = () => new Promise<void>((resolve) => { sleeps.push(resolve); });
      let claimed = false;
      const versioned = (status: number, body?: unknown) => json(status, body, { "x-cuelight-version": "0.3.0" });
      const routes: Record<string, Route> = {
        "POST /claims": () => versioned(201, claim),
        "GET /claims/claim-1": () => versioned(200, claimed ? { status: "claimed", credential } : { status: "pending" }),
        "PUT /player/capabilities": () => versioned(204),
        "GET /player/assignment": () => versioned(404, { detail: "no assignment" }),
      };
      const { controller } = harness(routes, undefined, sleep, { reloadDelayMs: 1000, reload: () => { reloads.push(controller.snapshot.view.phase); } });
      const pairing = waitFor(controller, (snapshot) => snapshot.view.phase === "pairing");
      controller.start();
      await pairing;
      await vi.advanceTimersByTimeAsync(5000);
      expect(reloads).toEqual([]);

      claimed = true;
      const idle = waitFor(controller, (snapshot) => snapshot.view.phase === "idle");
      sleeps.splice(0).forEach((resolve) => resolve());
      await idle;
      await vi.advanceTimersByTimeAsync(1000);
      expect(reloads).toEqual(["idle"]);
      controller.stop();

      // The reloaded page still sees 0.3.0 (say control serves an older build): it stays put.
      const again = harness(routes, undefined, sleep, { reloadDelayMs: 1000, reload: () => { reloads.push("again"); } });
      again.storage.save(credential);
      const polled = waitFor(again.controller, (snapshot) => snapshot.view.phase === "idle");
      again.controller.start();
      await polled;
      await vi.advanceTimersByTimeAsync(5000);
      again.controller.stop();
      expect(reloads).toEqual(["idle"]);
    } finally {
      vi.unstubAllGlobals();
      vi.useRealTimers();
    }
  });

  it("reports the Android shell's version and hands it a newer APK once", async () => {
    const installs: string[] = [];
    let assignmentCalls = 0;
    let thirdPoll = () => undefined as void;
    const polledThrice = new Promise<void>((resolve) => { thirdPoll = resolve; });
    const software = { version: "0.3.0", manifestUrl: "/m", artifactsBaseUrl: "/a/", apkUrl: "https://control.example/updates/0.3.0/Cuelight-0.3.0-android.apk", apkSha256: "b".repeat(64) };
    const softwareOnly = { id: "", nodeId: "node-1", releaseId: "", releaseDigest: "", desiredGeneration: 0, software };
    const { controller, storage, calls } = harness({
      "PUT /player/capabilities": () => json(204),
      "GET /player/assignment": (init) => {
        assignmentCalls += 1;
        if (assignmentCalls <= 2) return json(200, softwareOnly, { etag: `"assignment-0-sw-${assignmentCalls}"` });
        thirdPoll();
        return hang(init);
      },
    }, undefined, undefined, { android: { appVersion: () => "0.2.0", installUpdate: (body) => { installs.push(body); return true; } } });
    storage.save(credential);
    controller.start();
    await polledThrice;
    controller.stop();
    expect(JSON.parse(String(calls.find((call) => call.key === "PUT /player/capabilities")!.body))).toMatchObject({ softwareVersion: "0.2.0", updateMethod: "android" });
    expect(installs.map((body) => JSON.parse(body))).toEqual([{ version: "0.3.0", apkUrl: software.apkUrl, apkSha256: software.apkSha256 }]);
    expect(calls.some((call) => call.key.startsWith("GET /player/releases"))).toBe(false);
    expect(controller.snapshot.view.phase).toBe("idle");
  });
});

describe("presentation mapping", () => {
  it("keeps web URLs and maps schedule web items alongside media items", () => {
    const web = toPresentation({ ...release, payload: { ...release.payload, presentation: { kind: "web", web: { url: "https://example.com/" } } } }, (digest) => digest);
    expect(web).toMatchObject({ kind: "web", url: "https://example.com/", appAccess: false });
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
