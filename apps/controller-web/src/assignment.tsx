import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "@cable/api-client";
import { Button, Card, CardContent, CardHeader, CardTitle, ErrorState, useToast } from "@cable/controller-ui";
import { api, readApiResult } from "./api";
import { MediaTargetField, mediaKindLabel } from "./components/MediaPicker";

type Media = components["schemas"]["Media"];
export type DeliveryChoice = "" | "stream" | "cache_then_play";

export interface NodeAssignmentComposerProps {
  fixedNodeId?: string;
  /** Media shown by a detail page; the composer then only asks for the screen. */
  fixedMedia?: Media;
  title?: string;
}

export const fieldClass = "mt-1.5 h-10 w-full rounded-md border border-input bg-card px-3 text-sm font-normal";

async function nodes() {
  return readApiResult(await api.GET("/nodes", { params: { query: { limit: 100, sort: "name", order: "asc" } } }));
}

/** Delivery is only a choice for files; web pages always stream. */
export function DeliveryField({ media, value, onChange, disabled, label = "Delivery" }: { media?: Pick<Media, "kind">; value: DeliveryChoice; onChange: (value: DeliveryChoice) => void; disabled?: boolean; label?: string }) {
  if (media?.kind === "web") return null;
  return <label className="block text-sm font-medium">{label}<select className={fieldClass} value={value} disabled={disabled} onChange={(event) => onChange(event.currentTarget.value as DeliveryChoice)}>
    <option value="">Automatic</option>
    <option value="stream">Stream</option>
    <option value="cache_then_play">Cache then play</option>
  </select></label>;
}

export function NodeAssignmentComposer({ fixedNodeId, fixedMedia, title }: NodeAssignmentComposerProps) {
  const client = useQueryClient();
  const toast = useToast();
  const nodeQuery = useQuery({ queryKey: ["assignment-composer", "nodes"], queryFn: nodes });
  const [nodeId, setNodeId] = React.useState(fixedNodeId ?? "");
  const [picked, setPicked] = React.useState<Media | undefined>(undefined);
  const [deliveryMode, setDeliveryMode] = React.useState<DeliveryChoice>("");
  const [remoteInput, setRemoteInput] = React.useState(false);
  const [showQR, setShowQR] = React.useState(false);
  React.useEffect(() => { if (fixedNodeId) setNodeId(fixedNodeId); }, [fixedNodeId]);
  const media = fixedMedia ?? picked;

  const mutation = useMutation({
    mutationFn: async () => {
      if (!nodeId || !media) throw new Error("Choose both content and a screen.");
      const node = readApiResult(await api.GET("/nodes/{nodeId}", { params: { path: { nodeId } } }));
      return readApiResult(await api.PUT("/nodes/{nodeId}/assignment", {
        params: { path: { nodeId }, header: { "If-Match": "*", "Idempotency-Key": crypto.randomUUID() } },
        body: { mediaId: media.id, deliveryMode: media.kind === "web" || !deliveryMode ? undefined : deliveryMode, expectedGeneration: node.desiredGeneration ?? 0, remoteInput, showQr: remoteInput && showQR },
      }));
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["controller", "node-assignment", nodeId] });
      void client.invalidateQueries({ queryKey: ["controller", "node-status", nodeId] });
      void client.invalidateQueries({ queryKey: ["assignment-composer", "nodes"] });
      void client.invalidateQueries({ queryKey: ["profiles"] });
      toast.success("Assignment updated.");
    },
  });

  const pending = nodeQuery.isPending;
  return <Card className="w-full">{title && <CardHeader><CardTitle>{title}</CardTitle></CardHeader>}<CardContent className={title ? "space-y-5" : "space-y-5 pt-5"}>
    {nodeQuery.error instanceof Error && <ErrorState title="Assignment options unavailable" description={nodeQuery.error.message} onRetry={() => void nodeQuery.refetch()} />}
    {fixedMedia
      ? <p className="text-sm text-muted-foreground">{mediaKindLabel(fixedMedia)}: <strong className="text-foreground">{fixedMedia.title}</strong></p>
      : <MediaTargetField label="Content" value={picked?.id} onChange={setPicked} disabled={mutation.isPending} />}
    {!fixedNodeId && <label className="block text-sm font-medium">Screen<select className={fieldClass} value={nodeId} onChange={(event) => setNodeId(event.currentTarget.value)} disabled={pending}><option value="">Choose a screen</option>{nodeQuery.data?.items.map((node) => <option key={node.id} value={node.id}>{node.name}</option>)}</select></label>}
    <DeliveryField media={media} value={deliveryMode} onChange={setDeliveryMode} disabled={mutation.isPending} />
    <div className="space-y-3 border-t border-border pt-4">
      <label className="flex items-center gap-2 text-sm"><input className="h-4 w-4 accent-primary" type="checkbox" checked={remoteInput} onChange={(event) => { setRemoteInput(event.currentTarget.checked); if (!event.currentTarget.checked) setShowQR(false); }} />Allow remote control</label>
      {remoteInput && <label className="flex items-center gap-2 pl-6 text-sm text-muted-foreground"><input className="h-4 w-4 accent-primary" type="checkbox" checked={showQR} onChange={(event) => setShowQR(event.currentTarget.checked)} />Show a remote-control QR code on the screen</label>}
    </div>
    {mutation.error instanceof Error && <p role="alert" className="text-sm text-destructive">{mutation.error.message}</p>}
    <Button onClick={() => mutation.mutate()} disabled={pending || mutation.isPending || !nodeId || !media}>{mutation.isPending ? "Assigning…" : "Assign to screen"}</Button>
  </CardContent></Card>;
}
