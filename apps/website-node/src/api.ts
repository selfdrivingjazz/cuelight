import type {
  Assignment,
  BrowserCredential,
  AppToken,
  CapabilityReport,
  ClaimStarted,
  ClaimStatus,
  NodeReport,
  ReleaseEnvelope,
  RemoteEventPage,
  RemoteSession,
} from "./types";

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

/** Raised for HTTP failures; status 0 means the network request itself failed. */
export class ApiError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
    this.name = "ApiError";
  }
}

export type AssignmentPoll = { assignment: Assignment; etag: string } | { notModified: true } | { unassigned: true };

const BASE = "/api/v1/browser-node";

/** Browser-node HTTP API. Uses only fetch + JSON so it runs on TV browsers. */
export class BrowserNodeApi {
  constructor(private readonly origin = "", private readonly fetcher: FetchLike = (input, init) => fetch(input, init)) {}

  startClaim(userAgent: string): Promise<ClaimStarted> {
    return this.json<ClaimStarted>("POST", "/claims", undefined, { userAgent });
  }

  pollClaim(claimId: string, pollToken: string): Promise<ClaimStatus> {
    return this.json<ClaimStatus>("GET", `/claims/${encodeURIComponent(claimId)}`, pollToken);
  }

  renew(token: string): Promise<BrowserCredential> {
    return this.json<BrowserCredential>("POST", "/credentials:renew", token);
  }

  putCapabilities(token: string, report: CapabilityReport): Promise<void> {
    return this.json<void>("PUT", "/player/capabilities", token, report);
  }

  async pollAssignment(token: string, etag: string, waitSeconds: number, signal?: AbortSignal): Promise<AssignmentPoll> {
    const headers: Record<string, string> = { authorization: `Bearer ${token}` };
    if (etag) headers["if-none-match"] = etag;
    const response = await this.send("GET", `/player/assignment?waitSeconds=${waitSeconds}`, { headers, signal });
    if (response.status === 304) return { notModified: true };
    if (response.status === 404) return { unassigned: true };
    await ensureOk(response);
    return { assignment: (await response.json()) as Assignment, etag: response.headers.get("etag") || "" };
  }

  release(token: string, releaseId: string): Promise<ReleaseEnvelope> {
    return this.json<ReleaseEnvelope>("GET", `/player/releases/${encodeURIComponent(releaseId)}`, token);
  }

  report(token: string, report: NodeReport): Promise<void> {
    return this.json<void>("POST", "/player/reports", token, report);
  }

  createRemoteSession(token: string, desiredGeneration: number): Promise<RemoteSession> {
    return this.json<RemoteSession>("POST", "/player/remote-sessions", token, { desiredGeneration });
  }

  remoteEvents(token: string, sessionId: string, after: number, waitSeconds: number, signal?: AbortSignal): Promise<RemoteEventPage> {
    return this.json<RemoteEventPage>("GET", `/player/remote-sessions/${encodeURIComponent(sessionId)}/events?after=${after}&waitSeconds=${waitSeconds}`, token, undefined, signal);
  }

  /** Mints an app token for the appAccess web media item this node is assigned. */
  appToken(token: string, mediaId: string): Promise<AppToken> {
    return this.json<AppToken>("POST", "/player/app-tokens", token, { mediaId });
  }

  /** Media elements cannot send headers, so artifact URLs carry the token. */
  artifactUrl(token: string, digest: string): string {
    return `${this.origin}${BASE}/player/artifacts/${encodeURIComponent(digest)}/content?access_token=${encodeURIComponent(token)}`;
  }

  private async json<T>(method: string, path: string, token?: string, body?: unknown, signal?: AbortSignal): Promise<T> {
    const headers: Record<string, string> = { accept: "application/json" };
    if (token) headers.authorization = `Bearer ${token}`;
    if (body !== undefined) headers["content-type"] = "application/json";
    const response = await this.send(method, path, { headers, body: body === undefined ? undefined : JSON.stringify(body), signal });
    await ensureOk(response);
    if (response.status === 204 || response.status === 202) return undefined as T;
    return (await response.json()) as T;
  }

  private async send(method: string, path: string, init: RequestInit): Promise<Response> {
    try {
      return await this.fetcher(`${this.origin}${BASE}${path}`, { ...init, method, cache: "no-store", credentials: "omit" });
    } catch (error) {
      if (init.signal && init.signal.aborted) throw error;
      throw new ApiError(0, error instanceof Error ? error.message : "network request failed");
    }
  }
}

async function ensureOk(response: Response): Promise<void> {
  if (response.ok) return;
  let detail = "";
  try {
    const problem = (await response.json()) as { detail?: string; title?: string };
    detail = problem.detail || problem.title || "";
  } catch {
    // Non-JSON error bodies only carry the status.
  }
  throw new ApiError(response.status, detail || `request failed (${response.status})`);
}
