import { createRootRoute, createRoute, createRouter, useParams } from "@tanstack/react-router";
import { PlaylistAuthoringDetailPage, PlaylistAuthoringListPage, type AuthoringQuerySeam } from "./authoring-details";
import { api, readApiResult } from "./api";
import { MediaDetailPage, MediaLibraryPage, normalizeMediaSearch } from "./media-library";
import { RemoteControlPage } from "./phone-remote";
import { ActivityPage, AssetDetailPage, AssetsPage, OverviewPage, ReleaseDetailPage, ReleasesPage, SettingsPage, UploadsPage } from "./pages";
import { ProfileDetailPage, ProfilesPage } from "./profiles";
import { AssignPage, PairPage, ScreenDetailPage, ScreensPage, type ScreenTab } from "./screens";
import { Shell } from "./shell";
import { RouteError, RoutePending, normalizePageSearch, type NodeSearch } from "./kit";

const authoringQuerySeam: AuthoringQuerySeam = {
  async query(request) {
    const [mediaResult, usageResult] = await Promise.all([
      request.assetIds.length
        ? api.GET("/media", { params: { query: { assetId: [...request.assetIds], limit: 100, playable: true } } }).then(readApiResult)
        : Promise.resolve(undefined),
      api.GET("/usage/{kind}/{id}", { params: { path: { kind: request.kind, id: request.id }, query: { limit: 100 } } }).then(readApiResult),
    ]);
    return { media: mediaResult, usage: usageResult };
  },
};

/** Reads one path param from whichever matched route rendered the component. */
function useParam(name: string): string {
  const params = useParams({ strict: false }) as Record<string, string | undefined>;
  return params[name] ?? "";
}

function ScreenRoute({ tab }: { tab: ScreenTab }) { return <ScreenDetailPage nodeId={useParam("nodeId")} tab={tab} />; }

const rootRoute = createRootRoute({ component: Shell, pendingComponent: RoutePending, errorComponent: RouteError });
const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: "/", component: OverviewPage });
const remoteControlRoute = createRoute({ getParentRoute: () => rootRoute, path: "/remote/$sessionId", component: RemoteControlPage });

const screensRoute = createRoute({ getParentRoute: () => rootRoute, path: "/screens", validateSearch: (search: Record<string, unknown>): NodeSearch => ({ ...normalizePageSearch(search), sort: search.sort === "desiredGeneration" ? "desiredGeneration" : undefined }), component: ScreensPage });
const assignRoute = createRoute({ getParentRoute: () => rootRoute, path: "/screens/assign", component: AssignPage });
const pairRoute = createRoute({ getParentRoute: () => rootRoute, path: "/pair", validateSearch: (search: Record<string, unknown>): { code?: string } => ({ code: typeof search.code === "string" ? search.code : typeof search.code === "number" ? String(search.code) : undefined }), component: PairPage });
const screenRoute = createRoute({ getParentRoute: () => rootRoute, path: "/screens/$nodeId", component: () => <ScreenRoute tab="" /> });
const screenAssignmentRoute = createRoute({ getParentRoute: () => rootRoute, path: "/screens/$nodeId/assignment", component: () => <ScreenRoute tab="assignment" /> });
const screenRemoteRoute = createRoute({ getParentRoute: () => rootRoute, path: "/screens/$nodeId/remote", component: () => <ScreenRoute tab="remote" /> });
const screenEventsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/screens/$nodeId/events", component: () => <ScreenRoute tab="events" /> });
const screenSystemRoute = createRoute({ getParentRoute: () => rootRoute, path: "/screens/$nodeId/system", component: () => <ScreenRoute tab="system" /> });
const activityRoute = createRoute({ getParentRoute: () => rootRoute, path: "/activity", component: ActivityPage });
const profilesRoute = createRoute({ getParentRoute: () => rootRoute, path: "/profiles", component: ProfilesPage });
const profileDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/profiles/$profileId", component: () => <ProfileDetailPage profileId={useParam("profileId")} /> });

const mediaRoute = createRoute({ getParentRoute: () => rootRoute, path: "/library/media", validateSearch: normalizeMediaSearch, component: MediaLibraryPage });
const mediaDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/library/media/$mediaId", component: MediaDetailPage });
const playlistsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/library/playlists", component: PlaylistAuthoringListPage });
const playlistDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/library/playlists/$playlistId", component: () => <PlaylistAuthoringDetailPage playlistId={useParam("playlistId")} querySeam={authoringQuerySeam} /> });
const assetsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/library/assets", validateSearch: normalizePageSearch, component: AssetsPage });
const assetDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/library/assets/$assetId", component: () => <AssetDetailPage assetId={useParam("assetId")} /> });
const uploadsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/library/uploads", validateSearch: normalizePageSearch, component: UploadsPage });

const releasesRoute = createRoute({ getParentRoute: () => rootRoute, path: "/releases", validateSearch: normalizePageSearch, component: ReleasesPage });
const releaseDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/releases/$releaseId", component: () => <ReleaseDetailPage releaseId={useParam("releaseId")} /> });
const settingsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/settings", component: SettingsPage });

const routeTree = rootRoute.addChildren([
  indexRoute, remoteControlRoute,
  screensRoute, assignRoute, pairRoute, screenRoute, screenAssignmentRoute, screenRemoteRoute, screenEventsRoute, screenSystemRoute, activityRoute, profilesRoute, profileDetailRoute,
  mediaRoute, mediaDetailRoute, playlistsRoute, playlistDetailRoute, assetsRoute, assetDetailRoute, uploadsRoute,
  releasesRoute, releaseDetailRoute, settingsRoute,
]);
export const router = createRouter({ routeTree, defaultPreload: "intent" });
declare module "@tanstack/react-router" { interface Register { router: typeof router } }
export type AppRouter = typeof router;
