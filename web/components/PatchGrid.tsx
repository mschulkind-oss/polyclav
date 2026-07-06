"use client";

import { api } from "@/lib/api";
import { padColor } from "@/lib/padColors";
import type { Patch } from "@/lib/types";

interface PatchGridProps {
  patches: Patch[];
  current: string | null;
}

/** The pad-style patch selector: color chip + display name per patch. */
export function PatchGrid({ patches, current }: PatchGridProps) {
  return (
    <div className="grid">
      {patches.map((p) => (
        <button
          key={p.name}
          type="button"
          className={`patch${p.name === current ? " current" : ""}`}
          onClick={() => api.selectPatch(p.name)}
        >
          <span className="padcolor" style={{ background: padColor(p.pad_color) }} />
          <span>{p.display || p.name}</span>
        </button>
      ))}
    </div>
  );
}
