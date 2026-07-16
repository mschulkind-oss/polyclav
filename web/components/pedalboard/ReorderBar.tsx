"use client";

import type { CSSProperties, DragEvent, KeyboardEvent } from "react";
import { useState } from "react";
import type { PedalSpec } from "@/lib/pedalboard/model";
import { moveBy, moveRelative } from "@/lib/pedalboard/order";

export interface ReorderBarProps {
  /** Pedals already in display order (the parent applies the order). */
  pedals: PedalSpec[];
  /** Bypass map keyed by pedal id — dims a parked chip. */
  enabled: Record<string, boolean>;
  /** Fired with the full new id order on any reorder (drag or arrow keys). */
  onReorder: (order: string[]) => void;
}

/**
 * The reorder bar above the rail: one draggable chip per pedal, in chain order.
 * Drag a chip onto another (left half = before, right half = after) to move it,
 * or focus a chip and press ←/→ to nudge it. Reordering rearranges the board's
 * editing surface; the DSP signal path itself is fixed in the engine.
 */
export function ReorderBar({ pedals, enabled, onReorder }: ReorderBarProps) {
  const order = pedals.map((p) => p.id);
  const [dragId, setDragId] = useState<string | null>(null);
  const [overId, setOverId] = useState<string | null>(null);

  const onDrop = (targetId: string, e: DragEvent<HTMLButtonElement>) => {
    e.preventDefault();
    const id = dragId ?? e.dataTransfer.getData("text/plain");
    if (id) {
      const rect = e.currentTarget.getBoundingClientRect();
      const after = e.clientX > rect.left + rect.width / 2;
      onReorder(moveRelative(order, id, targetId, after));
    }
    setDragId(null);
    setOverId(null);
  };

  const onKey = (id: string, e: KeyboardEvent<HTMLButtonElement>) => {
    const delta = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
    if (delta === 0) return;
    e.preventDefault();
    onReorder(moveBy(order, id, delta));
  };

  return (
    <ol className="pb-reorder" aria-label="Pedal order — drag a chip or use arrow keys">
      {pedals.map((p, ix) => {
        const cls = ["pb-reorder-chip"];
        if (dragId === p.id) cls.push("pb-dragging");
        else if (overId === p.id && dragId) cls.push("pb-drop");
        if (!(enabled[p.id] ?? true)) cls.push("pb-bypassed");
        return (
          <li key={p.id} className="pb-reorder-slot">
            <button
              type="button"
              className={cls.join(" ")}
              style={{ "--pb-accent": `var(${p.accentVar})` } as CSSProperties}
              draggable
              aria-label={`${p.label} — position ${ix + 1} of ${order.length}; arrow keys to move`}
              onDragStart={(e) => {
                setDragId(p.id);
                e.dataTransfer.setData("text/plain", p.id);
                e.dataTransfer.effectAllowed = "move";
              }}
              onDragEnd={() => {
                setDragId(null);
                setOverId(null);
              }}
              onDragOver={(e) => {
                e.preventDefault();
                setOverId(p.id);
              }}
              onDragLeave={() => setOverId((cur) => (cur === p.id ? null : cur))}
              onDrop={(e) => onDrop(p.id, e)}
              onKeyDown={(e) => onKey(p.id, e)}
            >
              <span className="pb-reorder-dot" aria-hidden="true" />
              <span className="pb-reorder-name">{p.label}</span>
              <span className="pb-reorder-grip" aria-hidden="true">
                ⠿
              </span>
            </button>
          </li>
        );
      })}
    </ol>
  );
}
