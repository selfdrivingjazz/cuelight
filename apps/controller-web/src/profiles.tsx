import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import type { components } from "@cuelight/api-client";
import { Button, Card, CardContent, CardHeader, CardTitle, EmptyState, PageFrame, PageHeader, StatusBadge, TanStackDataTable, useToast } from "@cuelight/controller-ui";
import { api, ApiRequestError, readApiResult } from "./api";
import { useAuth } from "./auth";
import { DeliveryField, fieldClass, type DeliveryChoice } from "./assignment";
import { MediaTargetField } from "./components/MediaPicker";
import { QueryError, RoutePending, formatWhen, hasScope, listNodes } from "./kit";

type Profile = components["schemas"]["Profile"];
type ProfileScreen = components["schemas"]["ProfileScreen"];
type ProfileActivation = components["schemas"]["ProfileActivation"];
type ActiveProfile = components["schemas"]["ActiveProfile"];
type Media = components["schemas"]["Media"];

const profileKeys = {
  list: ["profiles", "list"] as const,
  active: ["profiles", "active"] as const,
  detail: (profileId: string) => ["profiles", "detail", profileId] as const,
};

const driftLabels: Record<ProfileDriftReason, string> = {
  assignment_changed: "Assignment changed since activation",
  unassigned: "Screen is no longer assigned",
  node_missing: "Screen was removed",
};
type ProfileDriftReason = components["schemas"]["ProfileDrift"]["reason"];

async function getActiveProfile(): Promise<ActiveProfile> {
  return readApiResult(await api.GET("/profiles/active"));
}

async function getProfile(profileId: string): Promise<{ profile: Profile; etag: string }> {
  const result = await api.GET("/profiles/{profileId}", { params: { path: { profileId } } });
  const profile = readApiResult(result);
  return { profile, etag: result.response.headers.get("ETag") ?? String(profile.version) };
}

/** Badge for a profile row: active (in sync or drifted) or nothing. */
function ActiveBadge({ profile, active }: { profile: Pick<Profile, "id" | "active">; active?: ActiveProfile }) {
  if (!profile.active) return null;
  const drifted = active?.profileId === profile.id && active.drifted;
  return drifted
    ? <StatusBadge tone="warning">Active · drifted</StatusBadge>
    : <StatusBadge tone="success">Active</StatusBadge>;
}

export function ProfilesPage() {
  const auth = useAuth();
  const navigate = useNavigate();
  const client = useQueryClient();
  const canWrite = hasScope(auth.operator, "assignment_write");
  const list = useQuery({ queryKey: profileKeys.list, queryFn: async () => readApiResult(await api.GET("/profiles")) });
  const active = useQuery({ queryKey: profileKeys.active, queryFn: getActiveProfile });
  const [creating, setCreating] = React.useState(false);
  const [name, setName] = React.useState("");
  const [description, setDescription] = React.useState("");
  const create = useMutation({
    mutationFn: async () => readApiResult(await api.POST("/profiles", { body: { name: name.trim(), description: description.trim() || undefined, screens: [] } })),
    onSuccess: (profile) => {
      void client.invalidateQueries({ queryKey: profileKeys.list });
      void navigate({ to: "/profiles/$profileId", params: { profileId: profile.id } });
    },
  });

  return <PageFrame>
    <PageHeader title="Profiles" description="Saved sets of screen assignments. Activating a profile assigns every screen in it at once; other screens are left alone.">
      {canWrite && <Button type="button" variant={creating ? "secondary" : "primary"} aria-expanded={creating} aria-controls="new-profile-form" onClick={() => setCreating((value) => !value)}>{creating ? "Cancel" : "New profile"}</Button>}
    </PageHeader>
    {creating && canWrite && <Card id="new-profile-form"><CardContent className="pt-5"><form className="grid gap-3 sm:grid-cols-[1fr_2fr_auto] sm:items-end" onSubmit={(event) => { event.preventDefault(); create.mutate(); }}>
      <label className="block text-sm font-medium">Name<input className={fieldClass} required maxLength={200} value={name} onChange={(event) => setName(event.currentTarget.value)} /></label>
      <label className="block text-sm font-medium">Description <span className="font-normal text-muted-foreground">(optional)</span><input className={fieldClass} value={description} onChange={(event) => setDescription(event.currentTarget.value)} /></label>
      <Button type="submit" disabled={create.isPending || !name.trim()}>{create.isPending ? "Creating…" : "Create profile"}</Button>
      {create.error instanceof Error && <p role="alert" className="text-sm text-destructive sm:col-span-3">{create.error.message}</p>}
    </form></CardContent></Card>}
    {active.data?.profileId && <Card><CardContent className="flex flex-wrap items-center gap-3 pt-5 text-sm">
      <span>Last activated: <Link className="font-medium text-primary hover:underline" to="/profiles/$profileId" params={{ profileId: active.data.profileId }}>{active.data.profileName ?? "Profile"}</Link> · {formatWhen(active.data.activatedAt)}</span>
      {active.data.drifted
        ? <StatusBadge tone="warning">{active.data.drift.length} {active.data.drift.length === 1 ? "screen has" : "screens have"} drifted</StatusBadge>
        : <StatusBadge tone="success">All screens match</StatusBadge>}
    </CardContent></Card>}
    {list.isPending ? <RoutePending /> : list.isError ? <QueryError error={list.error} retry={() => void list.refetch()} /> : <TanStackDataTable
      caption="Profiles"
      rows={list.data.items}
      rowKey={(profile) => profile.id}
      empty={<EmptyState title="No profiles" description={canWrite ? "Create a profile to save what each screen should show, then activate it in one step." : "No profiles have been created."} />}
      columns={[
        { key: "name", header: "Name", render: (profile) => <Link className="font-medium text-primary hover:underline" to="/profiles/$profileId" params={{ profileId: profile.id }}>{profile.name}</Link> },
        { key: "screens", header: "Screens", render: (profile) => <span className="tabular-nums">{profile.screens.length}</span> },
        { key: "lastActivatedAt", header: "Last activated", render: (profile) => formatWhen(profile.lastActivatedAt) },
        { key: "status", header: "Status", render: (profile) => <ActiveBadge profile={profile} active={active.data} /> },
      ]}
    />}
  </PageFrame>;
}

interface ScreenRow extends ProfileScreen { key: number }

function rowsFrom(screens: readonly ProfileScreen[]): ScreenRow[] {
  return screens.map((screen, index) => ({ ...screen, key: index }));
}

export function ProfileDetailPage({ profileId }: { profileId: string }) {
  const auth = useAuth();
  const toast = useToast();
  const navigate = useNavigate();
  const client = useQueryClient();
  const canWrite = hasScope(auth.operator, "assignment_write");
  const query = useQuery({ queryKey: profileKeys.detail(profileId), queryFn: () => getProfile(profileId) });
  const nodes = useQuery({ queryKey: ["controller", "nodes", { limit: 100 }], queryFn: () => listNodes({ limit: 100 }) });
  const active = useQuery({ queryKey: profileKeys.active, queryFn: getActiveProfile });
  const [name, setName] = React.useState("");
  const [description, setDescription] = React.useState("");
  const [rows, setRows] = React.useState<ScreenRow[]>([]);
  const [pickedKinds, setPickedKinds] = React.useState<Record<number, Media["kind"]>>({});
  const [nextKey, setNextKey] = React.useState(0);
  const [lastActivation, setLastActivation] = React.useState<ProfileActivation>();
  const loaded = query.data?.profile;
  React.useEffect(() => {
    if (!loaded) return;
    setName(loaded.name);
    setDescription(loaded.description);
    setRows(rowsFrom(loaded.screens));
    setNextKey(loaded.screens.length);
    setPickedKinds({});
    // Refetches return equal data; only a new version (save, edit elsewhere) replaces the draft.
  }, [loaded?.id, loaded?.version]);

  const screens: ProfileScreen[] = rows.map(({ key: _key, ...screen }) => ({ ...screen, showQr: screen.remoteInput && screen.showQr }));
  const dirty = Boolean(loaded) && (name !== loaded!.name || description !== loaded!.description || JSON.stringify(screens) !== JSON.stringify(loaded!.screens));
  const incomplete = rows.some((row) => !row.nodeId || (!row.mediaId && !row.playlistId));

  const invalidate = () => {
    void client.invalidateQueries({ queryKey: ["profiles"] });
  };
  const save = useMutation({
    mutationFn: async () => {
      const result = await api.PUT("/profiles/{profileId}", {
        params: { path: { profileId }, header: { "If-Match": query.data!.etag } },
        body: { name: name.trim(), description: description.trim() || undefined, screens },
      });
      const profile = readApiResult(result);
      return { profile, etag: result.response.headers.get("ETag") ?? String(profile.version) };
    },
    onSuccess: (updated) => {
      client.setQueryData(profileKeys.detail(profileId), updated);
      invalidate();
      toast.success("Profile saved.");
    },
  });
  const activate = useMutation({
    mutationFn: async () => readApiResult(await api.POST("/profiles/{profileId}/activate", { params: { path: { profileId }, header: { "Idempotency-Key": crypto.randomUUID() } } })),
    onSuccess: (activation) => {
      setLastActivation(activation);
      invalidate();
      for (const result of activation.results) {
        void client.invalidateQueries({ queryKey: ["controller", "node-assignment", result.nodeId] });
        void client.invalidateQueries({ queryKey: ["controller", "node-status", result.nodeId] });
      }
      const failed = activation.results.filter((result) => result.error).length;
      if (failed) toast.error(`${failed} of ${activation.results.length} screens could not be assigned.`);
      else toast.success(`Profile activated on ${activation.results.length} ${activation.results.length === 1 ? "screen" : "screens"}.`);
    },
  });
  const remove = useMutation({
    mutationFn: async () => {
      const result = await api.DELETE("/profiles/{profileId}", { params: { path: { profileId } } });
      if (!result.response.ok) throw new ApiRequestError(result.response.status, "The profile could not be deleted.");
    },
    onSuccess: () => {
      invalidate();
      toast.success("Profile deleted.");
      void navigate({ to: "/profiles" });
    },
  });

  if (query.isPending) return <RoutePending />;
  if (query.isError) return <PageFrame><PageHeader title="Profile" /><QueryError error={query.error} retry={() => void query.refetch()} /></PageFrame>;
  const profile = query.data.profile;
  const nodeName = new Map((nodes.data?.items ?? []).map((node) => [node.id, node.name]));
  const drift = active.data?.profileId === profile.id ? new Map(active.data.drift.map((item) => [item.nodeId, item.reason])) : new Map<string, ProfileDriftReason>();
  const usedNodes = new Set(rows.map((row) => row.nodeId));
  const update = (key: number, patch: Partial<ProfileScreen>) => setRows((current) => current.map((row) => row.key === key ? { ...row, ...patch } : row));
  const mutationError = save.error ?? activate.error ?? remove.error;

  return <PageFrame>
    <PageHeader
      eyebrow="Profile"
      title={profile.name}
      description={profile.description || undefined}
      meta={<div className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground"><ActiveBadge profile={profile} active={active.data} /><span>Last activated {formatWhen(profile.lastActivatedAt)}</span></div>}
    >
      {canWrite && <Button type="button" disabled={dirty || activate.isPending || profile.screens.length === 0} title={dirty ? "Save changes before activating." : undefined} onClick={() => activate.mutate()}>{activate.isPending ? "Activating…" : "Activate"}</Button>}
    </PageHeader>

    {lastActivation && <Card aria-labelledby="activation-results-heading"><CardHeader><CardTitle id="activation-results-heading">Activation results</CardTitle></CardHeader><CardContent>
      <ul className="space-y-2 text-sm">{lastActivation.results.map((result) => <li key={result.nodeId} className="flex flex-wrap items-center gap-2">
        {result.error ? <StatusBadge tone="danger">Failed</StatusBadge> : <StatusBadge tone="success">Assigned</StatusBadge>}
        <Link className="font-medium hover:text-primary" to="/screens/$nodeId" params={{ nodeId: result.nodeId }}>{nodeName.get(result.nodeId) ?? result.nodeId}</Link>
        {result.error && <span className="text-destructive">{result.error}</span>}
      </li>)}</ul>
    </CardContent></Card>}

    <Card><CardHeader><CardTitle>Screens</CardTitle></CardHeader><CardContent className="space-y-4">
      {canWrite && <div className="grid gap-3 sm:grid-cols-[1fr_2fr]">
        <label className="block text-sm font-medium">Name<input className={fieldClass} required value={name} onChange={(event) => setName(event.currentTarget.value)} /></label>
        <label className="block text-sm font-medium">Description<input className={fieldClass} value={description} onChange={(event) => setDescription(event.currentTarget.value)} /></label>
      </div>}
      {rows.length === 0 && <EmptyState title="No screens in this profile" description={canWrite ? "Add a screen and choose what it should show." : "This profile does not assign any screens."} />}
      <ul className="space-y-3" aria-label="Profile screens">{rows.map((row) => {
        const reason = row.nodeId ? drift.get(row.nodeId) : undefined;
        return <li key={row.key} className="space-y-3 rounded-lg border border-border p-3" aria-label={nodeName.get(row.nodeId) ?? "New screen"}>
          <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_minmax(0,2fr)_minmax(0,1fr)]">
            <label className="block text-sm font-medium">Screen<select className={fieldClass} value={row.nodeId} disabled={!canWrite} onChange={(event) => update(row.key, { nodeId: event.currentTarget.value })}>
              <option value="">Choose a screen</option>
              {(nodes.data?.items ?? []).filter((node) => node.id === row.nodeId || !usedNodes.has(node.id)).map((node) => <option key={node.id} value={node.id}>{node.name}</option>)}
            </select></label>
            {row.playlistId && !row.mediaId
              ? <p className="self-end text-sm text-muted-foreground">Playlist <Link className="text-primary hover:underline" to="/library/playlists/$playlistId" params={{ playlistId: row.playlistId }}>{row.playlistId.slice(0, 8)}</Link></p>
              : <MediaTargetField label="Content" value={row.mediaId} disabled={!canWrite} onChange={(media) => { update(row.key, { mediaId: media?.id, playlistId: undefined, deliveryMode: media?.kind === "web" ? undefined : row.deliveryMode }); setPickedKinds((current) => ({ ...current, [row.key]: media?.kind ?? "file" })); }} />}
            <DeliveryField media={pickedKinds[row.key] ? { kind: pickedKinds[row.key]! } : undefined} value={(row.deliveryMode ?? "") as DeliveryChoice} disabled={!canWrite} onChange={(value) => update(row.key, { deliveryMode: value || undefined })} />
          </div>
          <div className="flex flex-wrap items-center gap-4 text-sm">
            <label className="inline-flex items-center gap-2"><input className="h-4 w-4 accent-primary" type="checkbox" disabled={!canWrite} checked={row.remoteInput} onChange={(event) => update(row.key, { remoteInput: event.currentTarget.checked, showQr: event.currentTarget.checked && row.showQr })} />Allow remote control</label>
            {row.remoteInput && <label className="inline-flex items-center gap-2 text-muted-foreground"><input className="h-4 w-4 accent-primary" type="checkbox" disabled={!canWrite} checked={row.showQr} onChange={(event) => update(row.key, { showQr: event.currentTarget.checked })} />Show QR code</label>}
            {reason && <StatusBadge tone="warning">{driftLabels[reason]}</StatusBadge>}
            {canWrite && <Button type="button" variant="ghost" size="sm" className="ml-auto" onClick={() => setRows((current) => current.filter((candidate) => candidate.key !== row.key))}>Remove screen</Button>}
          </div>
        </li>;
      })}</ul>
      {canWrite && <div className="flex flex-wrap gap-2 border-t border-border pt-4">
        <Button type="button" variant="secondary" disabled={(nodes.data?.items ?? []).every((node) => usedNodes.has(node.id))} onClick={() => { setRows((current) => [...current, { key: nextKey, nodeId: "", remoteInput: false, showQr: false }]); setNextKey((value) => value + 1); }}>Add screen</Button>
        <Button type="button" disabled={!dirty || incomplete || !name.trim() || save.isPending} onClick={() => save.mutate()}>{save.isPending ? "Saving…" : "Save profile"}</Button>
        {dirty && <Button type="button" variant="ghost" onClick={() => { setName(profile.name); setDescription(profile.description); setRows(rowsFrom(profile.screens)); setPickedKinds({}); }}>Discard changes</Button>}
        <Button type="button" variant="ghost" className="ml-auto" disabled={remove.isPending} onClick={() => { if (window.confirm(`Delete profile “${profile.name}”? Screens keep their current assignments.`)) remove.mutate(); }}>Delete profile</Button>
      </div>}
      {incomplete && <p className="text-xs text-muted-foreground">Every row needs a screen and content before saving.</p>}
      {mutationError instanceof Error && <p role="alert" className="text-sm text-destructive">{mutationError instanceof ApiRequestError && mutationError.status === 412 ? "This profile changed elsewhere. Reload to see the latest version." : mutationError.message}</p>}
    </CardContent></Card>
  </PageFrame>;
}
