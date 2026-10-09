import type { DisplayBinding } from "../shared/contracts";

/** What the arbiter needs to know about one membership. */
export interface Contender {
  id: string;
  displayId: number;
  /** Sidecar process is alive. */
  running: boolean;
  /** Sidecar has an output loaded in its view. */
  hasOutput: boolean;
  pinned: boolean;
  /** `changedAt` (ms) of the last `POST /v1/assignment`, if any. */
  assignmentChangedAt?: number;
  /** Monotonic sequence of the last `POST /v1/assignment` arrival; breaks changedAt ties. */
  assignmentSeq?: number;
  /** Monotonic sequence of the current output's creation; orders contenders that never reported an assignment. */
  outputSeq?: number;
}

export interface Arbitration {
  /** display id -> membership id owning it. Displays nobody owns are absent. */
  onAir: Map<number, string>;
  /** Memberships that are connected while their display shows another network. */
  standby: Set<string>;
}

/** Resolves a display binding; `shared` and vanished displays follow the primary display. */
export function resolveDisplay(binding: DisplayBinding, displayIds: readonly number[], primaryId: number): number {
  return binding !== "shared" && displayIds.includes(binding) ? binding : primaryId;
}

/**
 * Decides which membership owns each display:
 * 1. a running pinned membership always wins its display (a local override);
 * 2. otherwise, among running memberships with an output, the most recent
 *    assignment change wins (then the latest arrival, then the newest output).
 * Displays are independent of each other.
 */
export function arbitrate(contenders: readonly Contender[]): Arbitration {
  const byDisplay = new Map<number, Contender[]>();
  for (const contender of contenders) {
    if (!contender.running) continue;
    const list = byDisplay.get(contender.displayId) ?? [];
    list.push(contender);
    byDisplay.set(contender.displayId, list);
  }
  const onAir = new Map<number, string>();
  const standby = new Set<string>();
  for (const [displayId, list] of byDisplay) {
    const pinned = list.filter((contender) => contender.pinned).sort(newestFirst)[0];
    const winner = pinned ?? list.filter((contender) => contender.hasOutput).sort(newestFirst)[0];
    if (!winner) continue;
    onAir.set(displayId, winner.id);
    for (const contender of list) if (contender.id !== winner.id) standby.add(contender.id);
  }
  return { onAir, standby };
}

function newestFirst(a: Contender, b: Contender): number {
  return (b.assignmentChangedAt ?? -Infinity) - (a.assignmentChangedAt ?? -Infinity)
    || (b.assignmentSeq ?? -Infinity) - (a.assignmentSeq ?? -Infinity)
    || (b.outputSeq ?? -Infinity) - (a.outputSeq ?? -Infinity);
}
