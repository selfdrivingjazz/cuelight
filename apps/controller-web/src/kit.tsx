import * as React from "react";
import { Link } from "@tanstack/react-router";
import { Card, CardContent, ErrorState, Skeleton, type NavigationLink } from "@cable/controller-ui";
import { api, ApiRequestError, readApiResult } from "./api";

export const RouterLink = Link as unknown as NavigationLink;

export interface PageSearch { cursor?: string; limit?: number; search?: string; sort?: string; order?: "asc" | "desc" }
export type NodeSearch = PageSearch & { sort?: "name" | "desiredGeneration" };

export const queryKeys = {
  health: ["controller", "health"] as const,
  operator: ["controller", "operator"] as const,
  nodes: (search: PageSearch) => ["controller", "nodes", search] as const,
  node: (nodeId: string) => ["controller", "node", nodeId] as const,
  nodeStatus: (nodeId: string) => ["controller", "node-status", nodeId] as const,
  nodeCapabilities: (nodeId: string) => ["controller", "node-capabilities", nodeId] as const,
  nodeAssignment: (nodeId: string) => ["controller", "node-assignment", nodeId] as const,
  nodeEvents: (nodeId: string, cursor?: string) => ["controller", "node-events", nodeId, cursor] as const,
  assets: (search: PageSearch) => ["controller", "assets", search] as const,
  asset: (assetId: string) => ["controller", "asset", assetId] as const,
  uploads: (search: PageSearch) => ["controller", "uploads", search] as const,
  releases: (search: PageSearch) => ["controller", "releases", search] as const,
  release: (releaseId: string) => ["controller", "release", releaseId] as const,
  activity: ["controller", "activity"] as const,
};

function pageSearch(search: PageSearch) { return { cursor: search.cursor, limit: search.limit ?? 25, search: search.search || undefined, sort: search.sort, order: search.order }; }
export async function listNodes(search: NodeSearch) { return readApiResult(await api.GET("/nodes", { params: { query: pageSearch({ ...search, sort: search.sort ?? "name" }) } })); }
export async function getNode(nodeId: string) { return readApiResult(await api.GET("/nodes/{nodeId}", { params: { path: { nodeId } } })); }
/** A screen that has never reported has no status (404); that is a state, not an error. */
export async function getNodeStatus(nodeId: string) {
  const result = await api.GET("/nodes/{nodeId}/status", { params: { path: { nodeId } } });
  return result.response.status === 404 ? null : readApiResult(result);
}
export async function getNodeCapabilities(nodeId: string) { return readApiResult(await api.GET("/nodes/{nodeId}/capabilities", { params: { path: { nodeId } } })); }
export async function getNodeAssignment(nodeId: string) { return readApiResult(await api.GET("/nodes/{nodeId}/assignment", { params: { path: { nodeId } } })); }
export async function listNodeEvents(nodeId: string, cursor?: string) { return readApiResult(await api.GET("/nodes/{nodeId}/events", { params: { path: { nodeId }, query: { cursor, limit: 50 } } })); }
export async function listAssets(search: PageSearch) { return readApiResult(await api.GET("/assets", { params: { query: pageSearch(search) } })); }
export async function getAsset(assetId: string) { return readApiResult(await api.GET("/assets/{assetId}", { params: { path: { assetId } } })); }
export async function listUploads(search: PageSearch) { return readApiResult(await api.GET("/uploads", { params: { query: pageSearch(search) } })); }
export async function getOperator() { return readApiResult(await api.GET("/operator/me")); }
export async function listReleases(search: PageSearch) { return readApiResult(await api.GET("/releases", { params: { query: pageSearch(search) } })); }
export async function getRelease(releaseId: string) { return readApiResult(await api.GET("/releases/{releaseId}", { params: { path: { releaseId } } })); }

/** Keeps only non-default values so list URLs stay clean (`/screens`, not `/screens?limit=25&order=asc`). */
export function normalizePageSearch(search: Record<string, unknown>): PageSearch { return { cursor: typeof search.cursor === "string" ? search.cursor : undefined, limit: typeof search.limit === "number" && search.limit !== 25 ? Math.min(Math.max(search.limit, 1), 100) : undefined, search: typeof search.search === "string" && search.search ? search.search : undefined, sort: typeof search.sort === "string" ? search.sort : undefined, order: search.order === "desc" ? "desc" : undefined }; }

export function QueryError({ error, retry }: { error: Error; retry: () => void }) { const status = error instanceof ApiRequestError ? error.status : undefined; const forbidden = status === 403; const notFound = status === 404; return <ErrorState title={forbidden ? "Access denied" : notFound ? "Not found" : "Unable to load data"} description={notFound ? "This resource may have been removed or you may not have access to it." : error.message} onRetry={retry} />; }
export function RoutePending() { return <main className="mx-auto w-full max-w-[var(--content-max-width)] space-y-5 px-4 pt-8 sm:px-6 md:px-8" aria-busy="true"><Skeleton className="h-4 w-24" /><Skeleton className="h-9 w-72" /><div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3"><Skeleton className="aspect-video w-full" /><Skeleton className="aspect-video w-full" /><Skeleton className="hidden aspect-video w-full xl:block" /></div></main>; }
export function RouteError({ error, reset }: { error: Error; reset: () => void }) { return <main className="p-6"><ErrorState title={error instanceof ApiRequestError && error.status === 403 ? "Access denied" : "Unable to load this page"} description={error.message || "The controller returned an unexpected error."} onRetry={reset} /></main>; }
export function LoadingCard() { return <Card><CardContent className="space-y-3 pt-5"><Skeleton className="h-4 w-40" /><Skeleton className="h-24 w-full" /></CardContent></Card>; }
export function SearchBox({ value, onChange, label }: { value: string; onChange: (value: string) => void; label: string }) {
  return <label className="relative block w-full max-w-sm text-sm"><span className="sr-only">{label}</span><svg aria-hidden="true" viewBox="0 0 24 24" className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" fill="none" stroke="currentColor" strokeWidth="2"><circle cx="11" cy="11" r="7" /><path d="m20 20-3.5-3.5" /></svg><input type="search" className="h-9 w-full rounded-md border border-input bg-card pl-9 pr-3" value={value} onChange={(event) => onChange(event.currentTarget.value)} placeholder={label} /></label>;
}
export function Toolbar({ children }: { children: React.ReactNode }) { return <div className="flex flex-wrap items-center justify-between gap-3">{children}</div>; }
export function formatWhen(value: string | undefined): string { if (!value) return "—"; const date = new Date(value); return Number.isNaN(date.valueOf()) ? value : date.toLocaleString([], { dateStyle: "medium", timeStyle: "short" }); }
export function shortId(value: string): string { return value.length > 12 ? `${value.slice(0, 8)}…` : value; }
export function hasScope(operator: { scopes: string[]; capabilities: Record<string, boolean> } | undefined, scope: string): boolean { return Boolean(operator && (operator.capabilities[scope] === true || operator.scopes.includes(scope) || operator.scopes.includes("admin"))); }
