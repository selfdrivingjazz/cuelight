import { describe, expect, it } from "vitest";
import { fetchRemoteEvents, GuideAuth, GuideAuthError, HostedPresentationSource, readGuideSession } from "./hosted";
import { loadPresentation, presentationFixture } from "./presentation";
import { globalRemoteAction } from "./remoteActions";
import { runtimeReducer, initialRuntimeState } from "./state";

function fakeHistory() {
  const calls: string[] = [];
  return { calls, state: { key: "x" }, replaceState(_data: unknown, _unused: string, url?: string | URL | null) { calls.push(String(url)); } };
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}

describe("guide session", () => {
  it("reads the deployment and token, then strips the fragment", () => {
    const history = fakeHistory();
    const session = readGuideSession({ pathname: "/cable/", search: "?deployment=dep-1", hash: "#token=abc%2Bdef" }, history);
    expect(session).toEqual({ deploymentId: "dep-1", token: "abc+def" });
    expect(history.calls).toEqual(["/cable/?deployment=dep-1"]);
  });

  it("rejects links without a token and still strips any fragment", () => {
    const history = fakeHistory();
    expect(() => readGuideSession({ pathname: "/cable/", search: "?deployment=dep-1", hash: "#other=1" }, history)).toThrow(GuideAuthError);
    expect(history.calls).toEqual(["/cable/?deployment=dep-1"]);
    expect(() => readGuideSession({ pathname: "/cable/", search: "", hash: "#token=abc" }, fakeHistory())).toThrow(/deployment/);
  });
});

describe("guide auth", () => {
  it("renews with the current token and adopts the new one", async () => {
    const requests: Array<{ url: string; init?: RequestInit }> = [];
    const expiresAt = new Date(Date.now() + 24 * 60 * 60_000).toISOString();
    const auth = new GuideAuth("old", async (input, init) => {
      requests.push({ url: String(input), init });
      return json({ token: "new", expiresAt });
    });
    expect(auth.renewDelay()).toBe(60 * 60_000);
    await auth.renew();
    expect(requests[0]?.url).toBe("/api/v1/cable-guide/token:renew");
    expect(requests[0]?.init).toMatchObject({ method: "POST", headers: { Authorization: "Bearer old" } });
    expect(auth.token).toBe("new");
    const delay = auth.renewDelay(Date.parse(expiresAt) - 60 * 60_000);
    expect(delay).toBe(50 * 60_000);
    expect(auth.renewDelay(Date.parse(expiresAt))).toBe(0);
  });

  it("treats a rejected renewal as lost access", async () => {
    const auth = new GuideAuth("old", async () => new Response(null, { status: 401 }));
    await expect(auth.renew()).rejects.toBeInstanceOf(GuideAuthError);
    expect(auth.error).toBeInstanceOf(GuideAuthError);
  });

  it("keeps the token after a transient renewal failure", async () => {
    const auth = new GuideAuth("old", async () => new Response(null, { status: 503 }));
    await expect(auth.renew()).rejects.not.toBeInstanceOf(GuideAuthError);
    expect(auth.error).toBeUndefined();
    expect(auth.token).toBe("old");
  });
});

describe("hosted presentation source", () => {
  it("fetches with the bearer token and signs media references", async () => {
    let init: RequestInit | undefined;
    let url = "";
    const auth = new GuideAuth("tok en");
    const source = new HostedPresentationSource(auth, async (input, requestInit) => {
      url = String(input);
      init = requestInit;
      return json(presentationFixture);
    });
    const presentation = await loadPresentation(source);
    expect(url).toBe("/api/v1/cable-guide/presentation");
    expect(init).toMatchObject({ credentials: "omit", cache: "no-store", headers: { Authorization: "Bearer tok en" } });
    expect(presentation.occurrences[0]?.media.reference).toBe(`${presentationFixture.occurrences[0]!.media.reference}?access_token=tok%20en`);
  });

  it("reports an expired token as lost access", async () => {
    const auth = new GuideAuth("expired");
    const source = new HostedPresentationSource(auth, async () => new Response(null, { status: 401 }));
    await expect(loadPresentation(source)).rejects.toBeInstanceOf(GuideAuthError);
    expect(auth.error).toBeDefined();
  });
});

describe("remote events", () => {
  it("long-polls the guide endpoint and maps the page", async () => {
    let url = "";
    const auth = new GuideAuth("tok");
    const page = await fetchRemoteEvents(auth, 4, 25, new AbortController().signal, async (input) => {
      url = String(input);
      return json({ events: [{ sequence: 5, type: "key", key: "Guide" }], next: 5, remote: { url: "https://control.example/r/2", showQr: true, expiresAt: new Date().toISOString() } });
    });
    expect(url).toBe("/api/v1/cable-guide/remote/events?after=4&wait=25");
    expect(page.next).toBe(5);
    expect(page.events.map((event) => event.key)).toEqual(["Guide"]);
    expect(page.remote?.url).toBe("https://control.example/r/2");
  });

  it("maps remote keys to guide actions", () => {
    const now = Date.parse(presentationFixture.occurrences[0]!.startsAt) + 1;
    const live = { ...presentationFixture, occurrences: presentationFixture.occurrences.map((occurrence) => ({ ...occurrence, startsAt: new Date(now - 1).toISOString(), endsAt: new Date(now + 60_000).toISOString() })) };
    const ready = runtimeReducer(initialRuntimeState, { type: "SYNC_REVISION", presentation: live });
    expect(globalRemoteAction(live, ready, "Guide", now)).toEqual({ type: "guide" });
    expect(globalRemoteAction(live, ready, "ChannelNumber:2", now)).toMatchObject({ type: "tune", occurrence: { channelId: "channel-2" } });
    expect(globalRemoteAction(live, ready, "ChannelDown", now)).toMatchObject({ type: "tune", occurrence: { channelId: "channel-2" } });
    const playing = runtimeReducer(ready, { type: "TUNE_LIVE", occurrence: live.occurrences[1]! });
    expect(globalRemoteAction(live, playing, "ChannelDown", now)).toMatchObject({ type: "tune", occurrence: { channelId: "channel-1" } });
    expect(globalRemoteAction(live, ready, "ChannelNumber:2", now + 120_000)).toEqual({ type: "off-air" });
    expect(globalRemoteAction(live, ready, "ArrowDown", now)).toBeUndefined();
    expect(globalRemoteAction(live, ready, "ChannelNumber:9", now)).toBeUndefined();
  });
});
