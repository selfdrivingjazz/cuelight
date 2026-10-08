import * as React from "react";

/** Returns a copy of `items` with the element at `from` moved so it lands at index `to`. Out-of-range or no-op moves return the same order. */
export function moveItem<T>(items: readonly T[], from: number, to: number): T[] {
  const next = [...items];
  if (from < 0 || from >= items.length || to < 0 || to >= items.length || from === to) return next;
  const [moved] = next.splice(from, 1);
  next.splice(to, 0, moved as T);
  return next;
}

/**
 * Maps a pointer position to a destination index given each row's vertical midpoint.
 * `midpoints` are the current on-screen centres of every row (including the dragged one) in order.
 */
export function dropIndexFor(midpoints: readonly number[], pointerY: number, from: number): number {
  let slot = midpoints.findIndex((mid) => pointerY < mid);
  if (slot < 0) slot = midpoints.length;
  // `slot` is an insertion gap in the original list; removing the dragged row shifts later gaps up by one.
  return slot > from ? slot - 1 : slot;
}

export interface SortableRenderState {
  index: number;
  dragging: boolean;
  /** Spread onto the drag handle element (a button). */
  handleProps: React.ButtonHTMLAttributes<HTMLButtonElement> & { "data-drag-handle": true };
}

export interface SortableListProps<T> {
  items: readonly T[];
  itemKey: (item: T) => string;
  itemLabel: (item: T) => string;
  onReorder: (next: T[], from: number, to: number) => void;
  renderItem: (item: T, state: SortableRenderState) => React.ReactNode;
  disabled?: boolean;
  label: string;
  className?: string;
  itemClassName?: string;
}

type DragState = { from: number; to: number; pointerId?: number; startY?: number; offsetY?: number; keyboard: boolean };

/**
 * Accessible reorderable list. Pointer/touch: press and drag the handle. Keyboard: focus the handle,
 * press Space/Enter to lift, Arrow keys (Home/End) to move, Space/Enter to drop, Escape to cancel.
 */
export function SortableList<T>({ items, itemKey, itemLabel, onReorder, renderItem, disabled = false, label, className, itemClassName }: SortableListProps<T>) {
  const [drag, setDrag] = React.useState<DragState | null>(null);
  const [announcement, setAnnouncement] = React.useState("");
  const rowRefs = React.useRef<Array<HTMLLIElement | null>>([]);
  const handleRefs = React.useRef<Array<HTMLButtonElement | null>>([]);
  const dragRef = React.useRef<DragState | null>(null);
  dragRef.current = drag;
  const instructionsId = React.useId();

  const name = (index: number) => { const item = items[index]; return item === undefined ? "item" : itemLabel(item); };
  const commit = (state: DragState) => {
    setDrag(null);
    if (state.from !== state.to) {
      onReorder(moveItem(items, state.from, state.to), state.from, state.to);
      setAnnouncement(`${name(state.from)} dropped at position ${state.to + 1} of ${items.length}.`);
    } else setAnnouncement(`${name(state.from)} dropped. Order unchanged.`);
    const target = state.to;
    requestAnimationFrame(() => handleRefs.current[target]?.focus());
  };

  // Row midpoints are captured at pickup so the dragged row's visual offset never feeds back into hit-testing.
  const midpoints = React.useRef<number[]>([]);
  const pointerTarget = (clientY: number, from: number) => dropIndexFor(midpoints.current, clientY, from);

  const handleProps = (index: number): SortableRenderState["handleProps"] => ({
    "data-drag-handle": true,
    type: "button",
    disabled,
    "aria-label": `Reorder ${name(index)}, position ${index + 1} of ${items.length}`,
    "aria-describedby": instructionsId,
    "aria-pressed": drag?.keyboard && drag.from === index ? true : undefined,
    style: { touchAction: "none" },
    onPointerDown: (event) => {
      if (disabled || event.button !== 0 || dragRef.current) return;
      event.preventDefault();
      event.currentTarget.setPointerCapture(event.pointerId);
      midpoints.current = rowRefs.current.slice(0, items.length).map((row) => { const rect = row?.getBoundingClientRect(); return rect ? rect.top + rect.height / 2 : 0; });
      setDrag({ from: index, to: index, pointerId: event.pointerId, startY: event.clientY, offsetY: 0, keyboard: false });
      setAnnouncement(`Picked up ${name(index)}.`);
    },
    onPointerMove: (event) => {
      const state = dragRef.current;
      if (!state || state.keyboard || state.pointerId !== event.pointerId) return;
      const to = pointerTarget(event.clientY, state.from);
      setDrag({ ...state, to, offsetY: event.clientY - (state.startY ?? event.clientY) });
    },
    onPointerUp: (event) => {
      const state = dragRef.current;
      if (!state || state.keyboard || state.pointerId !== event.pointerId) return;
      commit({ ...state, to: pointerTarget(event.clientY, state.from) });
    },
    onPointerCancel: () => { if (dragRef.current && !dragRef.current.keyboard) { setDrag(null); setAnnouncement("Reorder cancelled."); } },
    onKeyDown: (event) => {
      if (disabled) return;
      const state = dragRef.current;
      if (event.key === " " || event.key === "Enter") {
        event.preventDefault();
        if (!state) { setDrag({ from: index, to: index, keyboard: true }); setAnnouncement(`Picked up ${name(index)}, position ${index + 1} of ${items.length}. Use arrow keys to move, Space to drop, Escape to cancel.`); }
        else if (state.keyboard) commit(state);
        return;
      }
      if (!state?.keyboard) return;
      const last = items.length - 1;
      const to = event.key === "ArrowUp" || event.key === "ArrowLeft" ? Math.max(0, state.to - 1)
        : event.key === "ArrowDown" || event.key === "ArrowRight" ? Math.min(last, state.to + 1)
        : event.key === "Home" ? 0 : event.key === "End" ? last : null;
      if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); setDrag(null); setAnnouncement(`Reorder cancelled. ${name(state.from)} returned to position ${state.from + 1}.`); return; }
      if (to === null) return;
      event.preventDefault();
      if (to !== state.to) { setDrag({ ...state, to }); setAnnouncement(`${name(state.from)} moved to position ${to + 1} of ${items.length}.`); }
    },
    onBlur: () => { const state = dragRef.current; if (state?.keyboard) { setDrag(null); setAnnouncement("Reorder cancelled."); } },
  });

  // Drop indicator: a line in the gap the dragged row would occupy.
  const indicatorBefore = drag && drag.to !== drag.from ? (drag.to < drag.from ? drag.to : drag.to + 1) : null;
  return <>
    <p id={instructionsId} className="sr-only">Drag the handle, or press Space or Enter on it to lift, use the arrow keys to move, then Space or Enter to drop. Escape cancels.</p>
    <ol className={className} aria-label={label}>
      {items.map((item, index) => {
        const dragging = drag?.from === index;
        return <li
          key={itemKey(item)}
          ref={(node) => { rowRefs.current[index] = node; handleRefs.current[index] = node?.querySelector<HTMLButtonElement>("[data-drag-handle]") ?? null; }}
          data-dragging={dragging || undefined}
          style={dragging && drag.offsetY ? { transform: `translateY(${drag.offsetY}px)`, zIndex: 20 } : undefined}
          className={`relative ${itemClassName ?? ""} ${dragging ? "bg-card shadow-lg ring-2 ring-inset ring-primary" : ""}`}
        >
          {indicatorBefore === index && <DropIndicator position="top" />}
          {renderItem(item, { index, dragging, handleProps: handleProps(index) })}
          {indicatorBefore === items.length && index === items.length - 1 && <DropIndicator position="bottom" />}
        </li>;
      })}
    </ol>
    <p aria-live="assertive" className="sr-only">{announcement}</p>
  </>;
}

function DropIndicator({ position }: { position: "top" | "bottom" }) {
  return <span data-testid="drop-indicator" aria-hidden="true" className={`pointer-events-none absolute inset-x-1 z-30 h-1 rounded-full bg-primary ${position === "top" ? "top-0" : "bottom-0"}`} />;
}

export function DragHandleIcon({ className }: { className?: string }) {
  return <svg viewBox="0 0 24 24" fill="currentColor" className={className ?? "h-4 w-4"} aria-hidden="true"><circle cx="9" cy="6" r="1.6" /><circle cx="15" cy="6" r="1.6" /><circle cx="9" cy="12" r="1.6" /><circle cx="15" cy="12" r="1.6" /><circle cx="9" cy="18" r="1.6" /><circle cx="15" cy="18" r="1.6" /></svg>;
}
