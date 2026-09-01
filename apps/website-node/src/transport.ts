import type {
  Assignment,
  BrowserNodeTransport,
  ClaimResult,
  PlaybackReport,
} from "./types";

export type FetchLike = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

/** HTTP adapter with authenticated fetch-stream SSE; credentials never enter URLs. */
export class HttpBrowserNodeTransport implements BrowserNodeTransport {
  constructor(
    private readonly baseUrl = "",
    private readonly fetcher: FetchLike = fetch,
  ) {}

  async claim(nodeId: string, code: string): Promise<ClaimResult> {
    return this.post(`/api/v1/website-nodes/${encodeURIComponent(nodeId)}/claim`, { code });
  }

  async renew(nodeId: string, token: string): Promise<ClaimResult> {
    return this.post(`/api/v1/website-nodes/${encodeURIComponent(nodeId)}/credential/renew`, {}, token);
  }

  subscribe(
    nodeId: string,
    token: string,
    afterSequence: number,
    onAssignment: (assignment: Assignment) => void,
    onError: (error: unknown) => void,
  ): () => void {
    const url = new URL(`${this.baseUrl}/api/v1/website-nodes/${encodeURIComponent(nodeId)}/assignments`, window.location.origin);
    url.searchParams.set("after", String(afterSequence));
    const controller = new AbortController();
    void this.consumeAssignments(url, token, controller.signal, onAssignment).catch((error) => {
      if (!controller.signal.aborted) onError(error);
    });
    return () => controller.abort();
  }

  async acknowledge(nodeId: string, token: string, report: PlaybackReport): Promise<void> {
    await this.post(`/api/v1/website-nodes/${encodeURIComponent(nodeId)}/presence`, report, token);
  }

  private async consumeAssignments(url: URL, token: string, signal: AbortSignal, onAssignment: (assignment: Assignment) => void): Promise<void> {
    const response = await this.fetcher(url, { headers: { accept: "text/event-stream", authorization: `Bearer ${token}` }, cache: "no-store", credentials: "omit", signal });
    if (!response.ok) throw new Error(`website-node subscription failed (${response.status})`);
    if (!response.body) throw new Error("website-node subscription returned no event stream");
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    while (!signal.aborted) {
      const { done, value } = await reader.read();
      buffer += decoder.decode(value, { stream: !done }).replaceAll("\r\n", "\n");
      let boundary = buffer.indexOf("\n\n");
      while (boundary >= 0) {
        const block = buffer.slice(0, boundary);
        buffer = buffer.slice(boundary + 2);
        const data = block.split("\n").filter((line) => line.startsWith("data:")).map((line) => line.slice(5).trimStart()).join("\n");
        if (data) onAssignment(JSON.parse(data) as Assignment);
        boundary = buffer.indexOf("\n\n");
      }
      if (done) {
        if (!signal.aborted) throw new Error("website-node subscription closed");
        return;
      }
    }
  }

  private async post<T>(path: string, body: unknown, token?: string): Promise<T> {
    const response = await this.fetcher(`${this.baseUrl}${path}`, {
      method: "POST",
      headers: { "content-type": "application/json", ...(token ? { authorization: `Bearer ${token}` } : {}) },
      body: JSON.stringify(body),
    });
    if (!response.ok) throw new Error(`website-node request failed (${response.status})`);
    return response.json() as Promise<T>;
  }
}
