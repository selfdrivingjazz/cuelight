import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "@cable/api-client";
import { Button, Card, CardContent, CardHeader, CardTitle, ErrorState } from "@cable/controller-ui";
import { api, readApiResult } from "./api";

type Asset = components["schemas"]["Asset"];
type Release = components["schemas"]["ReleaseEnvelope"];
type Profile = components["schemas"]["CableProfile"];
type SourceKind = "media" | "profile" | "release";

export interface NodeAssignmentComposerProps {
  fixedNodeId?: string;
  fixedAsset?: Pick<Asset, "id" | "name">;
  fixedReleaseId?: string;
  title?: string;
}

const selectClass = "mt-1 h-10 w-full rounded-md border border-border bg-background px-3 text-sm";

async function nodes() {
  return readApiResult(await api.GET("/nodes", { params: { query: { limit: 100, sort: "name", order: "asc" } } }));
}
async function assets() {
  return readApiResult(await api.GET("/assets", { params: { query: { limit: 100, sort: "name", order: "asc" } } }));
}
async function profiles() {
  return readApiResult(await api.GET("/cable/profiles", { params: { query: { limit: 100 } } }));
}
async function release(id: string) {
  return readApiResult(await api.GET("/releases/{releaseId}", { params: { path: { releaseId: id } } }));
}
async function mediaRelease(assetId: string): Promise<Release> {
  return readApiResult(await api.POST("/releases/publish", {
    params: { header: { "Idempotency-Key": crypto.randomUUID() } },
    body: { playback: { assetId, fit: "fill", loop: true, muted: false, volume: 100, orientation: 0, allowedDeliveryModes: ["stream", "cache_then_play"] } },
  }));
}

export function NodeAssignmentComposer({ fixedNodeId, fixedAsset, fixedReleaseId, title = "Assign content" }: NodeAssignmentComposerProps) {
  const client = useQueryClient();
  const nodeQuery = useQuery({ queryKey: ["assignment-composer", "nodes"], queryFn: nodes });
  const assetQuery = useQuery({ queryKey: ["assignment-composer", "assets"], queryFn: assets, enabled: !fixedAsset && !fixedReleaseId });
  const profileQuery = useQuery({ queryKey: ["assignment-composer", "profiles"], queryFn: profiles, enabled: !fixedAsset && !fixedReleaseId });
  const [nodeId, setNodeId] = React.useState(fixedNodeId ?? "");
  const [kind, setKind] = React.useState<SourceKind>(fixedAsset ? "media" : fixedReleaseId ? "release" : "media");
  const [sourceId, setSourceId] = React.useState(fixedAsset?.id ?? fixedReleaseId ?? "");
  React.useEffect(() => { if (fixedNodeId) setNodeId(fixedNodeId); }, [fixedNodeId]);
  React.useEffect(() => {
    if (fixedAsset) { setKind("media"); setSourceId(fixedAsset.id); }
    else if (fixedReleaseId) { setKind("release"); setSourceId(fixedReleaseId); }
  }, [fixedAsset, fixedReleaseId]);

  const mutation = useMutation({
    mutationFn: async () => {
      if (!nodeId || !sourceId) throw new Error("Choose both content and a target node.");
      const node = nodeQuery.data?.items.find((item) => item.id === nodeId);
      let selectedRelease: Release;
      let deliveryMode: "stream" | "cache_then_play";
      if (kind === "media") {
        selectedRelease = await mediaRelease(sourceId);
        deliveryMode = "stream";
      } else if (kind === "profile") {
        const profile = profileQuery.data?.items.find((item: Profile) => item.id === sourceId);
        if (!profile?.releaseId) throw new Error("This Cable profile has no published release.");
        selectedRelease = await release(profile.releaseId);
        deliveryMode = "cache_then_play";
      } else {
        selectedRelease = await release(sourceId);
        deliveryMode = selectedRelease.payload.presentation?.kind === "cable" ? "cache_then_play" : "stream";
      }
      return readApiResult(await api.PUT("/nodes/{nodeId}/assignment", {
        params: { path: { nodeId }, header: { "If-Match": "*", "Idempotency-Key": crypto.randomUUID() } },
        body: { releaseId: selectedRelease.id, releaseDigest: selectedRelease.digest, deliveryMode, expectedGeneration: node?.desiredGeneration ?? 0 },
      }));
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["controller", "node-assignment", nodeId] });
      void client.invalidateQueries({ queryKey: ["controller", "node-status", nodeId] });
      void client.invalidateQueries({ queryKey: ["assignment-composer", "nodes"] });
    },
  });

  const pending = nodeQuery.isPending || assetQuery.isPending || profileQuery.isPending;
  const queryError = nodeQuery.error ?? assetQuery.error ?? profileQuery.error;
  const options = kind === "media" ? (assetQuery.data?.items ?? []).filter((item) => item.artifact.mediaType.startsWith("video/") || ["image/jpeg", "image/png", "image/webp"].includes(item.artifact.mediaType)) : kind === "profile" ? profileQuery.data?.items ?? [] : [];
  return <Card><CardHeader><CardTitle>{title}</CardTitle></CardHeader><CardContent className="space-y-4">
    {queryError instanceof Error && <ErrorState title="Assignment options unavailable" description={queryError.message} onRetry={() => { void nodeQuery.refetch(); void assetQuery.refetch(); void profileQuery.refetch(); }} />}
    {!fixedNodeId && <label className="block text-sm font-medium">Target node<select className={selectClass} value={nodeId} onChange={(event) => setNodeId(event.currentTarget.value)} disabled={pending}><option value="">Choose a node</option>{nodeQuery.data?.items.map((node) => <option key={node.id} value={node.id}>{node.name} · generation {node.desiredGeneration ?? 0}</option>)}</select></label>}
    {!fixedAsset && !fixedReleaseId && <label className="block text-sm font-medium">Content type<select className={selectClass} value={kind} onChange={(event) => { setKind(event.currentTarget.value as SourceKind); setSourceId(""); }}><option value="media">Media asset</option><option value="profile">Cable TV profile</option></select></label>}
    {!fixedAsset && !fixedReleaseId && <label className="block text-sm font-medium">{kind === "media" ? "Media" : "Cable TV profile"}<select aria-label={kind === "media" ? "Media" : "Cable TV profile"} className={selectClass} value={sourceId} onChange={(event) => setSourceId(event.currentTarget.value)} disabled={pending}><option value="">Choose {kind === "media" ? "media" : "a Cable profile"}</option>{options.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>}
    {fixedAsset && <p className="text-sm text-muted-foreground">Media: <strong className="text-foreground">{fixedAsset.name}</strong></p>}
    {fixedReleaseId && <p className="break-all text-sm text-muted-foreground">Release: <strong className="font-mono text-foreground">{fixedReleaseId}</strong></p>}
    {mutation.error instanceof Error && <p role="alert" className="text-sm text-destructive">{mutation.error.message}</p>}
    {mutation.isSuccess && <p role="status" className="text-sm text-status-success">Assignment advanced to generation {mutation.data.desiredGeneration}.</p>}
    <Button onClick={() => mutation.mutate()} disabled={pending || mutation.isPending || !nodeId || !sourceId}>{mutation.isPending ? "Assigning…" : "Assign to node"}</Button>
  </CardContent></Card>;
}
