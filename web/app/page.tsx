"use client";

// The pedalboard design system's stylesheets (scoped under .pb-root). Order
// matters: the foundation first, then each builder's .extra.css.
import "@/components/pedalboard/pedalboard.css";
import "@/components/pedalboard/chrome.extra.css";
import "@/components/pedalboard/composer.extra.css";
import "@/components/pedalboard/synth.extra.css";

import { useEffect, useRef, useState } from "react";
import { PatchBar } from "@/components/pedalboard/PatchBar";
import { Pedalboard } from "@/components/pedalboard/Pedalboard";
import { PedalEditor } from "@/components/pedalboard/PedalEditor";
import { ScaleControl, useUiScale } from "@/components/pedalboard/ScaleControl";
import { SynthScreen } from "@/components/SynthScreen";
import { SystemScreen } from "@/components/SystemScreen";
import { padColor } from "@/lib/padColors";
import { CHAIN, type PatchSpec, type PatchType } from "@/lib/pedalboard/model";
import { usePolyclav } from "./usePolyclav";

type Tab = "board" | "synth" | "system";
const TABS: { id: Tab; label: string }[] = [
  { id: "board", label: "Pedalboard" },
  { id: "synth", label: "Synth" },
  { id: "system", label: "System" },
];

export default function Page() {
  const pc = usePolyclav();
  const { state } = pc;
  const ui = useUiScale();
  const [tab, setTab] = useState<Tab>("board");
  const [editId, setEditId] = useState<string | null>(null);

  // The A−/A+ control resizes the whole system via --pb-scale on .pb-root.
  const rootRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    rootRef.current?.style.setProperty("--pb-scale", String(ui.scale));
  }, [ui.scale]);

  const currentPatch = state.patches.find((p) => p.name === state.current);
  const isNative = currentPatch?.type === "native";
  const patchName = currentPatch?.display || currentPatch?.name || state.current || "—";
  const patchSpecs: PatchSpec[] = state.patches.map((p) => ({
    name: p.display || p.name,
    type: p.type as PatchType,
    color: padColor(p.pad_color),
  }));
  const activeIx = state.patches.findIndex((p) => p.name === state.current);
  const editPedal = editId ? CHAIN.find((p) => p.id === editId) : undefined;

  const goBoard = () => {
    setTab("board");
    setEditId(null);
  };
  const resetPedal = (pedalId: string) => {
    const pedal = CHAIN.find((p) => p.id === pedalId);
    for (const param of pedal?.params ?? []) pc.setParam(param.id, param.defaultValue);
  };
  const screenClass = (active: boolean) => (active ? "pb-screen pb-active" : "pb-screen");

  return (
    <div className="pb-root" ref={rootRef}>
      <header className="pb-header">
        <div className="pb-brand">
          <div className="pb-chain-glyph" aria-hidden="true">
            <i />
            <i />
            <i />
            <i />
          </div>
          <span className="pb-wordmark">polyclav</span>
          <span
            className={`pb-chip${pc.connected ? " pb-live" : ""}`}
            title="Live daemon connection"
          >
            {pc.connected ? "live" : "offline"}
          </span>
        </div>
        <div className="pb-head-right">
          <div className="pb-head-chips">
            <span className="pb-chip">launchkey {state.devices.launchkey}</span>
            <span className="pb-chip">xr18 {state.devices.xr18}</span>
          </div>
          <nav className="pb-tabs" aria-label="Screens">
            {TABS.map((t) => (
              <button
                key={t.id}
                type="button"
                className={tab === t.id ? "pb-tab pb-active" : "pb-tab"}
                aria-pressed={tab === t.id}
                onClick={() => {
                  setTab(t.id);
                  if (t.id === "board") setEditId(null);
                }}
              >
                {t.label}
              </button>
            ))}
            <a className="pb-tab" href="/app/midi-probe/">
              MIDI Probe
            </a>
          </nav>
        </div>
      </header>

      <div className="pb-patchrow">
        <PatchBar
          patches={patchSpecs}
          activeIx={activeIx < 0 ? -1 : activeIx}
          onSelect={(ix) => pc.selectPatch(state.patches[ix].name)}
        />
      </div>

      <main className="pb-main">
        <section className={screenClass(tab === "board")}>
          {editPedal ? (
            <PedalEditor
              pedal={editPedal}
              values={state.chainValues}
              enabled={state.enabled[editPedal.id] ?? true}
              onChange={pc.setParam}
              onStomp={() => pc.togglePedal(editPedal.id)}
              onReset={() => resetPedal(editPedal.id)}
              onBack={goBoard}
            />
          ) : (
            <Pedalboard
              values={state.chainValues}
              enabled={state.enabled}
              order={state.order}
              onToggle={pc.togglePedal}
              onParamChange={pc.setParam}
              onReorder={pc.reorder}
              onOpenPedal={(id) => {
                setEditId(id);
                setTab("board");
              }}
              meta={pc.connected ? `${patchName} · live` : "offline · local"}
            />
          )}
        </section>

        <section className={screenClass(tab === "synth")}>
          <SynthScreen
            synth={state.synth}
            isNative={isNative}
            cutoffPos={state.cutoffPos}
            cutoffHz={state.cutoffHz}
            onCutoff={pc.setCutoff}
            patchName={patchName}
          />
        </section>

        <section className={screenClass(tab === "system")}>
          <SystemScreen
            velocityLabel={state.velocityLabel}
            onVelocity={pc.setVelocityLabel}
            noteSink={pc.noteSink}
            clips={pc.clips}
            player={state.player}
            onPlayer={pc.setPlayer}
          />
        </section>
      </main>

      <ScaleControl scale={ui.scale} onScale={ui.set} />

      <footer className="pb-footer">polyclav · flat modern · {state.version || "—"}</footer>
    </div>
  );
}
