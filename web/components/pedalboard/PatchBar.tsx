"use client";

import type { CSSProperties } from "react";
import { PAD_SLOTS, type PatchSpec } from "@/lib/pedalboard/model";

export interface PatchBarProps {
  patches: PatchSpec[];
  activeIx: number;
  onSelect: (ix: number) => void;
}

/**
 * Global patch row: PAD_SLOTS Launchkey-style pads. Occupied pads carry a
 * dim wash of their patch color; the active one fills solid with a glow.
 * Slots beyond patches.length are inert dashed placeholders (deliberately
 * empty, like the rail's empty role slots). Next to the pads: the active
 * patch's name and engine-type chip. Styles live in chrome.extra.css.
 */
export function PatchBar({ patches, activeIx, onSelect }: PatchBarProps) {
  const active = patches[activeIx];
  return (
    <div className="pb-patchbar">
      <div className="pb-pads" role="listbox" aria-label="Patches">
        {Array.from({ length: PAD_SLOTS }, (_, ix) => {
          const patch = patches[ix];
          if (!patch) {
            // biome-ignore lint/suspicious/noArrayIndexKey: empty slots are positional by nature
            return <span className="pb-pad pb-pad-empty" aria-hidden="true" key={`empty-${ix}`} />;
          }
          const isActive = ix === activeIx;
          return (
            <button
              type="button"
              role="option"
              key={patch.name}
              className={isActive ? "pb-pad pb-pad-active" : "pb-pad"}
              style={{ "--pb-pad-color": patch.color } as CSSProperties}
              aria-selected={isActive}
              aria-label={patch.name}
              title={patch.name}
              onClick={() => onSelect(ix)}
            />
          );
        })}
      </div>
      {active && (
        <div className="pb-patch-now">
          <span className="pb-patch-name">{active.name}</span>
          <span className="pb-chip">{active.type}</span>
        </div>
      )}
    </div>
  );
}
