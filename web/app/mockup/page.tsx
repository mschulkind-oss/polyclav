"use client";

import { useEffect, useRef } from "react";
import { type TabId, usePedalboardMock } from "@/app/mockup/state";
import { HwHintBar, type HwMapping } from "@/components/pedalboard/HwHintBar";
import { MacroGrid } from "@/components/pedalboard/MacroGrid";
import { PatchBar } from "@/components/pedalboard/PatchBar";
import { Pedalboard } from "@/components/pedalboard/Pedalboard";
import { PedalEditor } from "@/components/pedalboard/PedalEditor";
import { ScaleControl, useUiScale } from "@/components/pedalboard/ScaleControl";
import { SynthPanel } from "@/components/pedalboard/SynthPanel";
import { PATCHES } from "@/lib/pedalboard/model";

const TABS: { id: TabId; label: string }[] = [
  { id: "board", label: "Pedalboard" },
  { id: "editor", label: "Pedal editor" },
  { id: "synth", label: "Synth" },
  { id: "macros", label: "Macros" },
];

/** Where the delay editor lives on the Launchkey (reference hwbar). */
const DELAY_HW: HwMapping[] = [
  { k: "K1", label: "Time" },
  { k: "K2", label: "Feedback" },
  { k: "K3", label: "Mix" },
];

/**
 * The hidden design playground (/app/mockup): the full Flat Modern system —
 * header chrome, global patch row, and the four screens — composed from the
 * component packages over shared static mock state. Not linked from the nav.
 */
export default function MockupPage() {
  const ui = useUiScale();
  const mock = usePedalboardMock();

  // The A−/A+ control resizes the whole system: --pb-scale lives on the
  // layout's .pb-root wrapper, reached from any element inside it.
  const headerRef = useRef<HTMLElement | null>(null);
  useEffect(() => {
    headerRef.current
      ?.closest<HTMLElement>(".pb-root")
      ?.style.setProperty("--pb-scale", String(ui.scale));
  }, [ui.scale]);

  const patch = PATCHES[mock.patchIx];
  const screenClass = (id: TabId) => (mock.tab === id ? "pb-screen pb-active" : "pb-screen");

  return (
    <>
      <header className="pb-header" ref={headerRef}>
        <div className="pb-brand">
          <div className="pb-chain-glyph" aria-hidden="true">
            <i />
            <i />
            <i />
            <i />
          </div>
          <span className="pb-wordmark">polyclav</span>
          <span className="pb-badge">Flat Modern</span>
        </div>
        <div className="pb-head-right">
          <ScaleControl scale={ui.scale} onScale={ui.set} />
          <nav className="pb-tabs" aria-label="Screens">
            {TABS.map((t) => (
              <button
                key={t.id}
                type="button"
                className={mock.tab === t.id ? "pb-tab pb-active" : "pb-tab"}
                aria-pressed={mock.tab === t.id}
                onClick={() => mock.setTab(t.id)}
              >
                {t.label}
              </button>
            ))}
          </nav>
        </div>
      </header>

      <div className="pb-patchrow">
        <PatchBar patches={PATCHES} activeIx={mock.patchIx} onSelect={mock.setPatchIx} />
      </div>

      <main className="pb-main">
        <section className={screenClass("board")}>
          <Pedalboard
            values={mock.values}
            enabled={mock.enabled}
            onToggle={mock.togglePedal}
            onOpenPedal={() => mock.setTab("editor")}
          />
        </section>

        <section className={screenClass("editor")}>
          <PedalEditor
            values={{
              time: mock.values["delay.time_ms"],
              feedback: mock.values["delay.feedback"],
              mix: mock.values["delay.mix"],
            }}
            enabled={mock.enabled.delay}
            onChange={mock.setValue}
            onStomp={() => mock.togglePedal("delay")}
            onReset={() => mock.resetPedal("delay")}
            onBack={() => mock.setTab("board")}
          />
          <HwHintBar path="FX > Delay" mappings={DELAY_HW} />
        </section>

        <section className={screenClass("synth")}>
          <div className="pb-screen-head">
            <div>
              <div className="pb-kicker">Voice</div>
              <div className="pb-sub">Native synth engine · 3 oscillators · filter · env · LFO</div>
            </div>
            <div className="pb-meta pb-num">
              {patch.name} · {patch.type}
            </div>
          </div>
          <SynthPanel gated={patch.type !== "native"} patch={patch} />
        </section>

        <section className={screenClass("macros")}>
          <MacroGrid />
        </section>
      </main>

      <footer className="pb-footer">
        polyclav · flat modern · component playground · static mock data
      </footer>
    </>
  );
}
