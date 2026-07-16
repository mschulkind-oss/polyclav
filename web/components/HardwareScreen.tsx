"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { HwMap } from "@/lib/types";

export interface HardwareScreenProps {
  /** Fetch lazily the first time the Hardware tab is shown. */
  active: boolean;
}

/**
 * The Hardware Map screen (docs/PEDALBOARD_UI.md 3d, read-only slice): the
 * Launchkey's knob pages and what each encoder controls, served from
 * GET /api/hwmap — the "self-updating manual." The device-side Categories ×
 * Pages navigation (docs/LAUNCHKEY_NAVIGATION.md) is still a proposal, so this
 * is a reference of the current page layout, not a live follow view.
 */
export function HardwareScreen({ active }: HardwareScreenProps) {
  const [map, setMap] = useState<HwMap | null>(null);
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    if (!active || loaded) return;
    setLoaded(true);
    api.hwmap().then((m) => {
      if (m) setMap(m);
    });
  }, [active, loaded]);

  return (
    <>
      <div className="pb-screen-head">
        <div>
          <div className="pb-kicker">Hardware map</div>
          <div className="pb-sub">
            Launchkey knob pages — what each encoder controls (the self-updating manual)
          </div>
        </div>
        <div className="pb-meta pb-num">{map ? `${map.pages.length} pages · 8 knobs` : "—"}</div>
      </div>

      {map ? (
        <>
          <div className="pb-hwgrid">
            {map.pages.map((page) => (
              <div key={page.name} className="pb-panel pb-hwpage">
                <div className="pb-panel-head">
                  <h3>{page.name}</h3>
                </div>
                <ol className="pb-hwknobs">
                  {page.knobs
                    .map((label, i) => ({ key: `${page.name}-k${i + 1}`, num: i + 1, label }))
                    .map((r) => (
                      <li
                        key={r.key}
                        className={r.label ? "pb-hwknob" : "pb-hwknob pb-hwknob-empty"}
                      >
                        <span className="pb-hwk-n pb-num">K{r.num}</span>
                        <span className="pb-hwk-l">{r.label || "—"}</span>
                      </li>
                    ))}
                </ol>
              </div>
            ))}
          </div>
          <div className="pb-panel pb-panel-wide">
            <div className="pb-panel-head">
              <h3>Surface</h3>
            </div>
            <div className="pb-hw-line">
              <span className="pb-hw-k">Pads</span>
              {map.pads}
            </div>
            <div className="pb-hw-line">
              <span className="pb-hw-k">Transport</span>
              {map.transport}
            </div>
            {map.note ? <p className="pb-sub">{map.note}</p> : null}
          </div>
        </>
      ) : (
        <div className="pb-panel">
          <p className="pb-sub">
            {loaded ? "Hardware map unavailable (daemon offline or older build)." : "Loading…"}
          </p>
        </div>
      )}
    </>
  );
}
