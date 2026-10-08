import type { Occurrence, Presentation } from "./presentation";
import type { RuntimeState } from "./state";

/** Guide-wide action for a remote key, handled before the active page sees it. */
export type GlobalRemoteAction = { type: "guide" } | { type: "tune"; occurrence: Occurrence } | { type: "off-air" };

export function liveOccurrence(presentation: Presentation, channelId: string, now: number): Occurrence | undefined {
  return presentation.occurrences.find((candidate) => candidate.channelId === channelId && Date.parse(candidate.startsAt) <= now && now < Date.parse(candidate.endsAt));
}

/**
 * Maps Guide, ChannelNumber:N and ChannelUp/Down remote keys to guide actions.
 * Returns undefined when the key belongs to the active page (arrows, Enter,
 * volume, …); "off-air" consumes a channel key whose target has nothing live.
 */
export function globalRemoteAction(presentation: Presentation, state: RuntimeState, key: string | undefined, now = Date.now()): GlobalRemoteAction | undefined {
  if (key === "Guide") return { type: "guide" };
  const channels = presentation.channels.filter((channel) => channel.enabled);
  let target = key?.startsWith("ChannelNumber:")
    ? channels.find((channel) => channel.number === Number(key.slice("ChannelNumber:".length)))
    : undefined;
  if (!target && (key === "ChannelUp" || key === "ChannelDown") && channels.length > 0) {
    const playing = "occurrence" in state ? state.occurrence.channelId : undefined;
    const selected = state.kind === "ready" || state.kind === "stale-offline" ? state.selectedId : null;
    const currentId = playing ?? selected ?? presentation.occurrences[0]?.channelId ?? channels[0]?.id;
    const index = Math.max(0, channels.findIndex((channel) => channel.id === currentId));
    target = channels[(index + (key === "ChannelUp" ? -1 : 1) + channels.length) % channels.length];
  }
  if (!target) return undefined;
  const occurrence = liveOccurrence(presentation, target.id, now);
  return occurrence ? { type: "tune", occurrence } : { type: "off-air" };
}
