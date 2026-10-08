import * as React from "react";
import { Outlet, useRouter, useRouterState } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Activity, FolderArchive, Image, LayoutDashboard, Layers, ListVideo, MonitorPlay, PackageCheck, RadioTower, Search, Settings } from "lucide-react";
import { Breadcrumbs, CommandPalette, MobileNav, MobileSidebar, Sidebar, type CommandItem, type Crumb, type NavigationItem } from "@cable/controller-ui";
import "@cable/controller-ui/styles.css";
import { api, readApiResult } from "./api";
import { AccessScreen, useAuth } from "./auth";
import { RouterLink, RoutePending, shortId } from "./kit";

export const navigation: readonly NavigationItem[] = [
  { label: "Overview", href: "/", section: "Operate", icon: LayoutDashboard },
  { label: "Screens", href: "/screens", section: "Operate", icon: MonitorPlay, match: ["/pair"] },
  { label: "Profiles", href: "/profiles", section: "Operate", icon: Layers },
  { label: "Activity", href: "/activity", section: "Operate", icon: Activity },
  { label: "Media", href: "/library/media", section: "Library", icon: Image },
  { label: "Playlists", href: "/library/playlists", section: "Library", icon: ListVideo },
  { label: "Files", href: "/library/assets", section: "Library", icon: FolderArchive, match: ["/library/uploads"] },
  { label: "Deployments", href: "/programming/deployments", section: "Programming", icon: RadioTower, match: ["/programming/channels", "/programming/blocks", "/programming/programs"] },
  { label: "Releases", href: "/releases", section: "Delivery", icon: PackageCheck },
];

/** Static labels for URL patterns (`:id` = an entity segment). Patterns without an `href` have no list page. */
const crumbLabels: Record<string, { label: string; href?: string }> = {
  "/screens": { label: "Screens", href: "/screens" },
  "/screens/assign": { label: "Assign content" },
  "/pair": { label: "Pair a screen" },
  "/screens/:id/assignment": { label: "Assignment" },
  "/screens/:id/remote": { label: "Remote" },
  "/screens/:id/events": { label: "Events" },
  "/screens/:id/system": { label: "System" },
  "/library": { label: "Library" },
  "/library/media": { label: "Media", href: "/library/media" },
  "/library/playlists": { label: "Playlists", href: "/library/playlists" },
  "/library/assets": { label: "Files", href: "/library/assets" },
  "/library/uploads": { label: "Uploads", href: "/library/uploads" },
  "/programming": { label: "Programming" },
  "/programming/deployments": { label: "Deployments", href: "/programming/deployments" },
  "/programming/deployments/:id/channels": { label: "Channels" },
  "/profiles": { label: "Profiles", href: "/profiles" },
  "/programming/channels": { label: "Channel" },
  "/programming/channels/:id/programs": { label: "Programs" },
  "/programming/channels/:id/blocks": { label: "Blocks" },
  "/programming/blocks": { label: "Block" },
  "/programming/programs": { label: "Program" },
  "/releases": { label: "Releases", href: "/releases" },
  "/activity": { label: "Activity" },
  "/settings": { label: "Settings" },
};

type EntityKind = "screens" | "media" | "playlists" | "assets" | "deployments" | "channels" | "blocks" | "programs" | "profiles" | "releases";
const entityCollections: Record<string, EntityKind> = { screens: "screens", media: "media", playlists: "playlists", assets: "assets", deployments: "deployments", channels: "channels", blocks: "blocks", programs: "programs", profiles: "profiles", releases: "releases" };

/** Entity label plus its parents for entities whose URL is not nested under the parent (channels, blocks, programs). */
interface EntityTrail { name: string; parents: Crumb[] }

async function deploymentTrail(deploymentId: string): Promise<Crumb[]> {
  const deployment = readApiResult(await api.GET("/cable/deployments/{deploymentId}", { params: { path: { deploymentId } } }));
  return [{ label: "Deployments", href: "/programming/deployments" }, { label: deployment.name, href: `/programming/deployments/${deployment.id}` }];
}

async function channelTrail(channelId: string): Promise<Crumb[]> {
  const channel = readApiResult(await api.GET("/cable/channels/{channelId}", { params: { path: { channelId } } }));
  return [...await deploymentTrail(channel.deploymentId), { label: `${channel.number} · ${channel.name}`, href: `/programming/channels/${channel.id}` }];
}

async function entityTrail(kind: EntityKind, id: string): Promise<EntityTrail> {
  const path = { params: { path: { nodeId: id, mediaId: id, playlistId: id, assetId: id, deploymentId: id, channelId: id, blockId: id, programId: id, profileId: id, releaseId: id } } };
  switch (kind) {
    case "screens": return { name: readApiResult(await api.GET("/nodes/{nodeId}", path)).name, parents: [] };
    case "media": return { name: readApiResult(await api.GET("/media/{mediaId}", path)).title, parents: [] };
    case "playlists": return { name: readApiResult(await api.GET("/cable/playlists/{playlistId}", path)).name, parents: [] };
    case "assets": return { name: readApiResult(await api.GET("/assets/{assetId}", path)).name, parents: [] };
    case "deployments": return { name: readApiResult(await api.GET("/cable/deployments/{deploymentId}", path)).name, parents: [] };
    case "channels": { const channel = readApiResult(await api.GET("/cable/channels/{channelId}", path)); return { name: `${channel.number} · ${channel.name}`, parents: await deploymentTrail(channel.deploymentId) }; }
    case "blocks": { const block = readApiResult(await api.GET("/cable/blocks/{blockId}", path)); return { name: block.title, parents: [...await channelTrail(block.channelId), { label: "Blocks", href: `/programming/channels/${block.channelId}/blocks` }] }; }
    case "programs": { const program = readApiResult(await api.GET("/cable/programs/{programId}", path)); return { name: program.title || "Untitled program", parents: [...await channelTrail(program.channelId), { label: "Programs", href: `/programming/channels/${program.channelId}/programs` }] }; }
    case "profiles": return { name: readApiResult(await api.GET("/profiles/{profileId}", path)).name, parents: [] };
    case "releases": return { name: shortId(id), parents: [] };
  }
}

interface CrumbSegment { pattern: string; path: string; entity?: { kind: EntityKind; id: string } }

/** Splits a pathname into breadcrumb segments; an entity id is any segment directly after a known collection. */
export function crumbSegments(pathname: string): CrumbSegment[] {
  const parts = pathname.split("/").filter(Boolean);
  const segments: CrumbSegment[] = [];
  const pattern: string[] = [];
  for (const [index, part] of parts.entries()) {
    const previous = parts[index - 1];
    const path = `/${parts.slice(0, index + 1).join("/")}`;
    const literal = `/${[...pattern, part].join("/")}`;
    const kind = previous ? entityCollections[previous] : undefined;
    if (kind && !crumbLabels[literal]) {
      pattern.push(":id");
      segments.push({ pattern: `/${pattern.join("/")}`, path, entity: { kind, id: decodeURIComponent(part) } });
    } else {
      pattern.push(part);
      segments.push({ pattern: literal, path });
    }
  }
  return segments;
}

function EntityCrumbs({ segments }: { segments: CrumbSegment[] }) {
  const entities = segments.flatMap((segment) => segment.entity ? [segment.entity] : []);
  const names = useQuery({
    queryKey: ["crumbs", entities],
    queryFn: async () => Promise.all(entities.map((entity) => entityTrail(entity.kind, entity.id).catch((): EntityTrail => ({ name: shortId(entity.id), parents: [] })))),
    enabled: entities.length > 0,
    staleTime: 60_000,
  });
  let entityIndex = 0;
  const items: Crumb[] = segments.flatMap((segment, index) => {
    const last = index === segments.length - 1;
    if (segment.entity) {
      const trail = names.data?.[entityIndex];
      entityIndex += 1;
      return [...(trail?.parents ?? []), { label: trail?.name ?? "…", href: last ? undefined : segment.path }];
    }
    const known = crumbLabels[segment.pattern];
    if (!known) return [];
    if (!known.href && !last && segments[index + 1]?.entity) return [];
    return [{ label: known.label, href: last ? undefined : known.href }];
  });
  return <Breadcrumbs items={items.length ? items : [{ label: "Overview" }]} linkComponent={RouterLink} />;
}

function usePaletteItems(open: boolean): { items: CommandItem[]; loading: boolean } {
  const data = useQuery({
    queryKey: ["command-palette"],
    enabled: open,
    staleTime: 30_000,
    queryFn: async () => {
      const [nodes, media, deployments, playlists, profiles] = await Promise.allSettled([
        api.GET("/nodes", { params: { query: { limit: 100 } } }).then(readApiResult),
        api.GET("/media", { params: { query: { limit: 100 } } }).then(readApiResult),
        api.GET("/cable/deployments", { params: { query: { limit: 100 } } }).then(readApiResult),
        api.GET("/cable/playlists", { params: { query: { limit: 100 } } }).then(readApiResult),
        api.GET("/profiles").then(readApiResult),
      ]);
      const items: CommandItem[] = [];
      if (nodes.status === "fulfilled") items.push(...nodes.value.items.map((node) => ({ id: `screen-${node.id}`, label: node.name, group: "Screens", href: `/screens/${node.id}`, hint: node.nodeClass === "browser" ? "browser" : "desktop" })));
      if (media.status === "fulfilled") items.push(...media.value.items.map((item) => ({ id: `media-${item.id}`, label: item.title, group: "Media", href: `/library/media/${item.id}`, hint: item.kind === "web" ? (item.deploymentId ? "Cable guide" : "Web page") : item.artifact?.mediaType })));
      if (playlists.status === "fulfilled") items.push(...playlists.value.items.map((item) => ({ id: `playlist-${item.id}`, label: item.name, group: "Playlists", href: `/library/playlists/${item.id}` })));
      if (deployments.status === "fulfilled") items.push(...deployments.value.items.map((item) => ({ id: `deployment-${item.id}`, label: item.name, group: "Deployments", href: `/programming/deployments/${item.id}`, hint: item.timezone })));
      if (profiles.status === "fulfilled") items.push(...profiles.value.items.map((item) => ({ id: `profile-${item.id}`, label: item.name, group: "Profiles", href: `/profiles/${item.id}`, hint: item.active ? "active" : undefined })));
      return items;
    },
  });
  const pages: CommandItem[] = [
    ...navigation.map((item) => ({ id: `page-${item.href}`, label: item.label, group: "Go to", href: item.href, hint: item.section })),
    { id: "action-pair", label: "Pair a screen", group: "Actions", href: "/pair" },
    { id: "action-assign", label: "Assign content to a screen", group: "Actions", href: "/screens/assign" },
    { id: "action-add-web", label: "Add a web page", group: "Actions", href: "/library/media?add=web" },
    { id: "page-settings", label: "Settings", group: "Go to", href: "/settings" },
  ];
  return { items: [...pages, ...(data.data ?? [])], loading: data.isPending };
}

/** Same-origin plain anchors (cards, usage references) navigate inside the SPA instead of reloading. */
function useSpaAnchors(): (event: React.MouseEvent) => void {
  const router = useRouter();
  return React.useCallback((event: React.MouseEvent) => {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const anchor = (event.target as HTMLElement).closest("a");
    const href = anchor?.getAttribute("href");
    if (!anchor || !href || !href.startsWith("/") || href.startsWith("//") || anchor.target || anchor.hasAttribute("download")) return;
    event.preventDefault();
    router.history.push(href);
  }, [router]);
}

export function Shell() {
  const auth = useAuth();
  const router = useRouter();
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const [paletteOpen, setPaletteOpen] = React.useState(false);
  const palette = usePaletteItems(paletteOpen);
  const onClick = useSpaAnchors();
  React.useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") { event.preventDefault(); setPaletteOpen((open) => !open); }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  if (pathname.startsWith("/remote/")) return <Outlet />;
  if (auth.state === "signedOut" || auth.state === "invalid") return <AccessScreen />;
  if (auth.state === "checking" || !auth.operator) return <RoutePending />;
  const footer = <RouterLink to="/settings" aria-current={pathname === "/settings" ? "page" : undefined} className="flex items-center gap-3 rounded-md px-3 py-2 text-sm text-sidebar-foreground/65 hover:bg-sidebar-accent hover:text-sidebar-foreground"><Settings className="h-4 w-4" aria-hidden />Settings<span className="ml-auto truncate font-mono text-[11px] opacity-60">{shortId(auth.operator.id)}</span></RouterLink>;
  return <div className="min-h-screen bg-background text-foreground" onClick={onClick}>
    <a className="sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-4 focus:z-50 focus:rounded-md focus:bg-primary focus:px-4 focus:py-2 focus:text-primary-foreground" href="#main-content">Skip to content</a>
    <div className="flex min-h-screen">
      <Sidebar items={navigation} currentPath={pathname} linkComponent={RouterLink} footer={footer} />
      <div className="min-w-0 flex-1">
        <header className="sticky top-0 z-30 flex h-14 items-center gap-3 border-b border-border bg-background/85 px-4 backdrop-blur sm:px-6 md:px-8">
          <MobileNav key={pathname}><MobileSidebar items={navigation} currentPath={pathname} linkComponent={RouterLink} footer={footer} /></MobileNav>
          <div className="min-w-0 flex-1"><EntityCrumbs segments={crumbSegments(pathname)} /></div>
          <button type="button" onClick={() => setPaletteOpen(true)} className="inline-flex h-8 items-center gap-2 rounded-md border border-input bg-card px-2.5 text-sm text-muted-foreground hover:text-foreground" aria-label="Jump to"><Search className="h-4 w-4" aria-hidden /><span className="hidden sm:inline">Jump to…</span><kbd className="hidden rounded border border-border px-1 font-mono text-[10px] md:inline">⌘K</kbd></button>
        </header>
        <Outlet />
      </div>
    </div>
    <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} items={palette.items} loading={palette.loading} onSelect={(item) => router.history.push(item.href)} />
  </div>;
}
