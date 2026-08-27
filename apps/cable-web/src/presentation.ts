import { z } from "zod";

const mediaSchema = z.discriminatedUnion("kind", [z.object({ kind: z.literal("video"), reference: z.string().min(1), mime: z.literal("video/mp4") }), z.object({ kind: z.literal("image"), reference: z.string().min(1), mime: z.enum(["image/jpeg", "image/png", "image/webp"]) })]);
const occurrenceSchema = z.object({ id: z.string(), channelId: z.string(), startsAt: z.string().datetime(), endsAt: z.string().datetime(), title: z.string(), subtitle: z.string().optional(), media: mediaSchema, placard: z.enum(["always", "start", "never"]) });
const channelSchema = z.object({ id: z.string(), number: z.number().int().positive(), name: z.string(), callsign: z.string(), accent: z.string(), enabled: z.boolean(), standbyMessage: z.string().optional() });
export const presentationSchema = z.object({ revision: z.object({ id: z.string(), digest: z.string(), generatedAt: z.string().datetime(), timezone: z.string(), stale: z.boolean() }), channels: z.array(channelSchema), occurrences: z.array(occurrenceSchema), bridge: z.object({ protocolVersion: z.string(), state: z.enum(["online", "offline", "stale"]) }), theme: z.enum(["nebula", "gallery", "noir"]) });
export type Presentation = z.infer<typeof presentationSchema>;
export type Channel = Presentation["channels"][number];
export type Occurrence = Presentation["occurrences"][number];

export interface PresentationSource { read(): Promise<unknown> }
export class PresentationError extends Error { constructor(message: string) { super(message); this.name = "PresentationError"; } }

export async function loadPresentation(source: PresentationSource): Promise<Presentation> { const result = presentationSchema.safeParse(await source.read()); if (!result.success) throw new PresentationError("The node bridge returned an invalid presentation revision."); return result.data; }

export const presentationFixture: Presentation = { revision: { id: "revision-demo", digest: "sha256:demo-revision", generatedAt: "2026-08-27T17:00:00.000Z", timezone: "UTC", stale: false }, bridge: { protocolVersion: "1", state: "online" }, theme: "nebula", channels: [{ id: "channel-1", number: 1, name: "Main", callsign: "MAIN", accent: "#5eead4", enabled: true }, { id: "channel-2", number: 2, name: "Gallery", callsign: "GALL", accent: "#7dd3fc", enabled: true }], occurrences: [{ id: "program-1", channelId: "channel-1", startsAt: "2026-08-27T18:00:00.000Z", endsAt: "2026-08-27T19:00:00.000Z", title: "Signal / Sea", subtitle: "A visual interlude", media: { kind: "video", reference: "bridge://media/signal-sea", mime: "video/mp4" }, placard: "start" }, { id: "program-2", channelId: "channel-2", startsAt: "2026-08-27T18:30:00.000Z", endsAt: "2026-08-27T19:30:00.000Z", title: "Night archive", media: { kind: "image", reference: "bridge://media/night-archive", mime: "image/png" }, placard: "always" }] };
