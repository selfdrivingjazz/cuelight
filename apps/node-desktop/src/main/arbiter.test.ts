import { describe, expect, test } from "vitest";
import { arbitrate, resolveDisplay, type Contender } from "./arbiter";

const PRIMARY = 1;
const SIDE = 2;

function contender(id: string, patch: Partial<Contender> = {}): Contender {
  return { id, displayId: PRIMARY, running: true, hasOutput: true, pinned: false, ...patch };
}

describe("arbitrate", () => {
  test("the most recent assignment change wins a shared display; the other is standby", () => {
    const result = arbitrate([
      contender("tailnet", { assignmentChangedAt: Date.parse("2026-10-08T10:00:00Z"), assignmentSeq: 2 }),
      contender("mars", { assignmentChangedAt: Date.parse("2026-10-08T10:05:00Z"), assignmentSeq: 1 }),
    ]);
    expect(result.onAir.get(PRIMARY)).toBe("mars");
    expect([...result.standby]).toEqual(["tailnet"]);
  });

  test("a sidecar restart re-reporting an old changedAt does not steal the display", () => {
    // tailnet reported later (higher arrival seq) but its change is older.
    const result = arbitrate([
      contender("mars", { assignmentChangedAt: 2_000, assignmentSeq: 1 }),
      contender("tailnet", { assignmentChangedAt: 1_000, assignmentSeq: 9 }),
    ]);
    expect(result.onAir.get(PRIMARY)).toBe("mars");
  });

  test("a pin overrides a newer assignment, even before the pinned network has output", () => {
    const result = arbitrate([
      contender("tailnet", { pinned: true, hasOutput: false, assignmentChangedAt: 1_000 }),
      contender("mars", { assignmentChangedAt: 5_000 }),
    ]);
    expect(result.onAir.get(PRIMARY)).toBe("tailnet");
    expect(result.standby.has("mars")).toBe(true);
  });

  test("a stopped pinned network does not hold the display", () => {
    const result = arbitrate([contender("tailnet", { pinned: true, running: false }), contender("mars")]);
    expect(result.onAir.get(PRIMARY)).toBe("mars");
    expect(result.standby.size).toBe(0);
  });

  test("a newer assignment without output yet leaves the current output on air", () => {
    const result = arbitrate([
      contender("tailnet", { assignmentChangedAt: 1_000 }),
      contender("mars", { assignmentChangedAt: 5_000, hasOutput: false }),
    ]);
    expect(result.onAir.get(PRIMARY)).toBe("tailnet");
    expect(result.standby.has("mars")).toBe(true);
  });

  test("displays are arbitrated independently", () => {
    const result = arbitrate([
      contender("tailnet", { displayId: PRIMARY, assignmentChangedAt: 1_000 }),
      contender("mars", { displayId: SIDE, assignmentChangedAt: 5_000 }),
      contender("lab", { displayId: SIDE, assignmentChangedAt: 3_000, pinned: true }),
    ]);
    expect(result.onAir.get(PRIMARY)).toBe("tailnet");
    expect(result.onAir.get(SIDE)).toBe("lab");
    expect([...result.standby]).toEqual(["mars"]);
  });

  test("nobody is standby on a display with no output", () => {
    const result = arbitrate([contender("tailnet", { hasOutput: false }), contender("mars", { hasOutput: false })]);
    expect(result.onAir.size).toBe(0);
    expect(result.standby.size).toBe(0);
  });

  test("without assignment reports, the newest output wins", () => {
    expect(arbitrate([contender("a", { outputSeq: 4 }), contender("b", { outputSeq: 7 })]).onAir.get(PRIMARY)).toBe("b");
  });
});

test("bindings to a vanished display fall back to the primary display", () => {
  expect(resolveDisplay(SIDE, [PRIMARY, SIDE], PRIMARY)).toBe(SIDE);
  expect(resolveDisplay(SIDE, [PRIMARY], PRIMARY)).toBe(PRIMARY);
  expect(resolveDisplay("shared", [PRIMARY, SIDE], PRIMARY)).toBe(PRIMARY);
});
