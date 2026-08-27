import createClient, { type Client } from "openapi-fetch";
import type { components, paths } from "./generated";

export type { components, paths } from "./generated";
export type Page = components["schemas"]["PageInfo"];
export type Node = components["schemas"]["Node"];
export type Asset = components["schemas"]["Asset"];
export type Problem = components["schemas"]["Problem"];

export interface ListQuery {
  cursor?: string;
  limit?: number;
  search?: string;
  sort?: string;
  order?: "asc" | "desc";
}

export interface ApiConfig {
  baseUrl: string;
  token?: string;
  fetch?: typeof globalThis.fetch;
}

export type ApiClient = Client<paths>;

export function createApiClient(config: ApiConfig): ApiClient {
  return createClient<paths>({
    baseUrl: config.baseUrl,
    fetch: config.fetch ?? globalThis.fetch,
    headers: config.token ? { Authorization: `Bearer ${config.token}` } : undefined,
  });
}

export interface PageResult<T> {
  items: T[];
  page: Page;
}

export const emptyPage: Page = { nextCursor: null, previousCursor: null, hasMore: false };

export const nodeFixtures: Node[] = [
  { id: "node-001", name: "Lobby display", credentialState: "active", desiredGeneration: 12, observedGeneration: 12 },
  { id: "node-002", name: "Studio west", credentialState: "active", desiredGeneration: 9, observedGeneration: 8 },
  { id: "node-003", name: "Outdoor kiosk", credentialState: "active", desiredGeneration: 3, observedGeneration: 0 },
];

export const assetFixtures: Asset[] = [
  { id: "asset-001", name: "Welcome loop", artifact: { digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", size: 48_000_000, mediaType: "video/mp4", state: "ready" } },
  { id: "asset-002", name: "Evening ident", artifact: { digest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", size: 8_000_000, mediaType: "video/mp4", state: "ready" } },
];

export function fixturePage<T>(items: T[], query: ListQuery = {}): PageResult<T> {
  const limit = Math.min(Math.max(query.limit ?? 25, 1), 100);
  const search = query.search?.trim().toLocaleLowerCase();
  const filtered = search ? items.filter((item) => JSON.stringify(item).toLocaleLowerCase().includes(search)) : items;
  const start = query.cursor ? Number.parseInt(query.cursor, 10) || 0 : 0;
  const pageItems = filtered.slice(start, start + limit);
  return { items: pageItems, page: { nextCursor: start + limit < filtered.length ? String(start + limit) : null, previousCursor: start > 0 ? String(Math.max(start - limit, 0)) : null, hasMore: start + limit < filtered.length } };
}
