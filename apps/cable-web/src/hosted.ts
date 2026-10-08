import type { RemoteDescriptor, RemoteEvent } from "./components/RemoteInput";
import { PresentationError, presentationSchema, type PresentationSource } from "./presentation";

/** Control-hosted guide API (contract §2). */
export const guideAPI = {
  presentation: "/api/v1/cable-guide/presentation",
  renew: "/api/v1/cable-guide/token:renew",
  remoteEvents: "/api/v1/cable-guide/remote/events",
} as const;

export class GuideAuthError extends PresentationError {
  constructor(message: string) {
    super(message);
    this.name = "GuideAuthError";
  }
}

export interface GuideSession {
  deploymentId: string;
  token: string;
}

interface LocationLike { pathname: string; search: string; hash: string }
interface HistoryLike { state: unknown; replaceState(data: unknown, unused: string, url?: string | URL | null): void }

/**
 * Reads `?deployment=<id>#token=<guideToken>` and strips the fragment so the
 * token does not linger in the address bar or history. Throws GuideAuthError
 * when either part is missing.
 */
export function readGuideSession(location: LocationLike, history: HistoryLike): GuideSession {
  const fragment = new URLSearchParams(location.hash.replace(/^#/, ""));
  const token = fragment.get("token")?.trim() ?? "";
  if (location.hash) history.replaceState(history.state, "", `${location.pathname}${location.search}`);
  const deploymentId = new URLSearchParams(location.search).get("deployment")?.trim() ?? "";
  if (!deploymentId) throw new GuideAuthError("This guide link is missing its deployment.");
  if (!token) throw new GuideAuthError("This guide link is missing its access token. Reassign the Cable guide to this screen.");
  return { deploymentId, token };
}

type Fetcher = typeof fetch;

const renewMargin = 10 * 60_000;
const renewFallback = 60 * 60_000;

/** Holds the guide token and renews it before it expires. */
export class GuideAuth {
  private expiresAt: number | undefined;
  private failed: GuideAuthError | undefined;

  constructor(private current: string, private readonly fetcher: Fetcher = (input, init) => fetch(input, init)) {}

  get token(): string {
    return this.current;
  }

  /** Error recorded once the token has been rejected or could not be renewed before expiry. */
  get error(): GuideAuthError | undefined {
    return this.failed;
  }

  headers(): Record<string, string> {
    return { Authorization: `Bearer ${this.current}`, Accept: "application/json" };
  }

  /** Delay until the next renewal: 10 minutes before expiry, or hourly when expiry is unknown. */
  renewDelay(now = Date.now()): number {
    if (this.expiresAt === undefined) return renewFallback;
    return Math.max(0, this.expiresAt - renewMargin - now);
  }

  async renew(): Promise<void> {
    let response: Response;
    try {
      response = await this.fetcher(guideAPI.renew, { method: "POST", cache: "no-store", credentials: "omit", headers: this.headers() });
    } catch {
      this.expireIfPast();
      throw new Error("The guide token could not be renewed.");
    }
    if (response.status === 401 || response.status === 403) throw this.reject();
    if (!response.ok) {
      this.expireIfPast();
      throw new Error(`Guide token renewal returned HTTP ${response.status}.`);
    }
    const body = await response.json() as { token?: string; expiresAt?: string };
    if (!body.token) throw new Error("Guide token renewal returned no token.");
    this.current = body.token;
    const expiresAt = body.expiresAt ? Date.parse(body.expiresAt) : NaN;
    this.expiresAt = Number.isNaN(expiresAt) ? undefined : expiresAt;
  }

  /** Marks the token rejected by the server. */
  reject(): GuideAuthError {
    this.failed ??= new GuideAuthError("This guide's access has expired. Reassign the Cable guide to this screen.");
    return this.failed;
  }

  private expireIfPast() {
    if (this.expiresAt !== undefined && this.expiresAt <= Date.now()) this.reject();
  }

  /** Renews on schedule until stopped. Retries failed renewals after a minute. */
  start(onError: (error: GuideAuthError) => void): () => void {
    let timer: number | undefined;
    let stopped = false;
    const schedule = (delay: number) => {
      timer = window.setTimeout(() => {
        void this.renew().then(() => { if (!stopped) schedule(this.renewDelay()); }, (error: unknown) => {
          if (stopped) return;
          if (error instanceof GuideAuthError) onError(error);
          else schedule(60_000);
        });
      }, delay);
    };
    schedule(this.renewDelay());
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }
}

/** Appends `access_token` so <video>/<img> can fetch guide media without headers. */
export function withAccessToken(reference: string, token: string): string {
  const [path, fragment] = reference.split("#", 2);
  const separator = path!.includes("?") ? "&" : "?";
  return `${path}${separator}access_token=${encodeURIComponent(token)}${fragment === undefined ? "" : `#${fragment}`}`;
}

/** Reads the live presentation from control; media references carry the guide token. */
export class HostedPresentationSource implements PresentationSource {
  constructor(private readonly auth: GuideAuth, private readonly fetcher: Fetcher = (input, init) => fetch(input, init)) {}

  async read(): Promise<unknown> {
    if (this.auth.error) throw this.auth.error;
    let response: Response;
    try {
      response = await this.fetcher(guideAPI.presentation, { credentials: "omit", cache: "no-store", headers: this.auth.headers() });
    } catch {
      throw new PresentationError("The Cable guide could not be reached.");
    }
    if (response.status === 401) throw this.auth.reject();
    if (!response.ok) throw new PresentationError(`The Cable guide returned HTTP ${response.status}.`);
    let payload: unknown;
    try {
      payload = await response.json();
    } catch {
      throw new PresentationError("The Cable guide returned invalid JSON.");
    }
    const parsed = presentationSchema.safeParse(payload);
    if (!parsed.success) return payload;
    const token = this.auth.token;
    return { ...parsed.data, occurrences: parsed.data.occurrences.map((occurrence) => ({ ...occurrence, media: { ...occurrence.media, reference: withAccessToken(occurrence.media.reference, token) } })) };
  }
}

export interface RemoteEventPage {
  events: RemoteEvent[];
  next: number;
  remote?: RemoteDescriptor;
}

/** One long-poll of the node's remote session through the guide API. */
export async function fetchRemoteEvents(auth: GuideAuth, after: number, waitSeconds: number, signal: AbortSignal, fetcher: Fetcher = (input, init) => fetch(input, init)): Promise<RemoteEventPage> {
  const response = await fetcher(`${guideAPI.remoteEvents}?after=${after}&wait=${waitSeconds}`, { cache: "no-store", credentials: "omit", headers: auth.headers(), signal });
  if (response.status === 401) throw auth.reject();
  if (!response.ok) throw new Error(`Remote events returned HTTP ${response.status}.`);
  const page = await response.json() as Partial<RemoteEventPage>;
  return { events: page.events ?? [], next: page.next ?? after, remote: page.remote };
}
