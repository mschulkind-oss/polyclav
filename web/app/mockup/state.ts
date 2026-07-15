"use client";

import { useCallback, useState } from "react";
import { INITIAL_ENABLED } from "@/components/pedalboard/Pedalboard";
import { CHAIN } from "@/lib/pedalboard/model";

/** The four playground screens, in tab order. */
export type TabId = "board" | "editor" | "synth" | "macros";

export interface PedalboardMock {
  tab: TabId;
  setTab: (tab: TabId) => void;
  /** Active patch index into PATCHES (0 = Minimoog, the native default). */
  patchIx: number;
  setPatchIx: (ix: number) => void;
  /** Pedal param values keyed by param id — shared by the board and editor. */
  values: Record<string, number>;
  setValue: (paramId: string, value: number) => void;
  /** Back to the spec defaults for one pedal (the editor's Reset button). */
  resetPedal: (pedalId: string) => void;
  /** Stomp state per pedal id — shared by the board strips and the editor. */
  enabled: Record<string, boolean>;
  togglePedal: (pedalId: string) => void;
}

/** Spec defaults (= the reference's live values) for one pedal or the chain. */
function chainDefaults(pedalId?: string): Record<string, number> {
  return Object.fromEntries(
    CHAIN.filter((pedal) => pedalId === undefined || pedal.id === pedalId).flatMap((pedal) =>
      pedal.params.map((p) => [p.id, p.defaultValue]),
    ),
  );
}

/**
 * All cross-screen playground state: pedal values + bypass (so editor edits
 * reflect straight back into the board's minis), the active patch, and the
 * active tab. Synth-voice and macro-sweep state live inside SynthPanel and
 * MacroGrid respectively — those screens are self-contained by design.
 * Static mock data only; nothing here talks to the API.
 */
export function usePedalboardMock(): PedalboardMock {
  const [tab, setTab] = useState<TabId>("board");
  const [patchIx, setPatchIx] = useState(0);
  const [values, setValues] = useState(chainDefaults);
  const [enabled, setEnabled] = useState(INITIAL_ENABLED);

  const setValue = useCallback((paramId: string, value: number) => {
    setValues((prev) => ({ ...prev, [paramId]: value }));
  }, []);
  const resetPedal = useCallback((pedalId: string) => {
    setValues((prev) => ({ ...prev, ...chainDefaults(pedalId) }));
  }, []);
  const togglePedal = useCallback((pedalId: string) => {
    setEnabled((prev) => ({ ...prev, [pedalId]: !(prev[pedalId] ?? true) }));
  }, []);

  return {
    tab,
    setTab,
    patchIx,
    setPatchIx,
    values,
    setValue,
    resetPedal,
    enabled,
    togglePedal,
  };
}
