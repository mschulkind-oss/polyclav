import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { SSEHandlers } from "@/lib/useSSE";

// Capture the SSE handler map so tests can drive events directly, and stub the
// REST client so we can assert what the hook sends (no daemon, no hardware).
let handlers: SSEHandlers = {};
vi.mock("@/lib/useSSE", () => ({
  useSSE: (_url: string, h: SSEHandlers) => {
    handlers = h;
    return true;
  },
}));
vi.mock("@/lib/api", () => ({
  api: {
    chain: vi.fn(async () => null),
    clips: vi.fn(async () => null),
    selectPatch: vi.fn(),
    patchParams: vi.fn(),
    patchMastering: vi.fn(),
    patchChain: vi.fn(),
  },
}));

import { api } from "@/lib/api";
import { usePolyclav } from "./usePolyclav";

const mocked = api as unknown as Record<string, ReturnType<typeof vi.fn>>;

beforeEach(() => {
  handlers = {};
  for (const fn of Object.values(mocked)) if (typeof fn.mockClear === "function") fn.mockClear();
  window.localStorage.clear();
});
afterEach(() => vi.useRealTimers());

test("snapshot seeds patches, devices, and master/reverb/comp values", () => {
  const { result } = renderHook(() => usePolyclav());
  act(() => {
    handlers.snapshot?.({
      version: "1.2.3",
      devices: { launchkey: "connected", xr18: "connected" },
      patches: [
        { name: "Moog", display: "Moog", type: "native", pad_color: 41, gain_db: 0, index: 0 },
      ],
      params: {
        patch: "Moog",
        volume: 0.8, // -> master.level 80
        reverb: 0.5, // -> reverb.mix 50
        compressor: 0.3, // -> comp.amount 30
        mastering_comp: 0.2, // -> comp.glue 20
        limiter_ceiling_db: -1, // -> master.ceiling -1
        cutoff_pos: 0.4,
        cutoff_hz: 800,
      },
      player: null,
    });
  });
  const s = result.current.state;
  expect(s.version).toBe("1.2.3");
  expect(s.current).toBe("Moog");
  expect(s.chainValues["master.level"]).toBe(80);
  expect(s.chainValues["reverb.mix"]).toBe(50);
  expect(s.chainValues["comp.amount"]).toBe(30);
  expect(s.chainValues["comp.glue"]).toBe(20);
  expect(s.chainValues["master.ceiling"]).toBe(-1);
});

test("a chain SSE event (engine units) updates the display value", () => {
  const { result } = renderHook(() => usePolyclav());
  act(() => handlers.chain?.({ field: "chorus.mix", value: 0.4, patch: "Moog" }));
  expect(result.current.state.chainValues["chorus.mix"]).toBe(40);
});

test("a chain enable event flips the pedal (tremolo -> trem)", () => {
  const { result } = renderHook(() => usePolyclav());
  act(() => handlers.chain?.({ field: "tremolo.enabled", value: true, patch: "Moog" }));
  expect(result.current.state.enabled.trem).toBe(true);
});

test("setParam sends the converted value to the right endpoint after debounce", () => {
  vi.useFakeTimers();
  const { result } = renderHook(() => usePolyclav());
  act(() => result.current.setParam("reverb.mix", 40)); // reverb.mix -> /api/params {reverb}
  expect(result.current.state.chainValues["reverb.mix"]).toBe(40); // optimistic
  act(() => vi.advanceTimersByTime(120));
  expect(mocked.patchParams).toHaveBeenCalledWith({ reverb: 0.4 });
});

test("setParam on a chain pedal batches to /api/chain in engine units", () => {
  vi.useFakeTimers();
  const { result } = renderHook(() => usePolyclav());
  act(() => result.current.setParam("delay.feedback", 45)); // 45% -> 0.45
  act(() => vi.advanceTimersByTime(120));
  expect(mocked.patchChain).toHaveBeenCalledWith({ "delay.feedback": 0.45 });
});

test("toggling a chain pedal sends its enable; toggling reverb parks the param", () => {
  const { result } = renderHook(() => usePolyclav());
  // chorus starts enabled -> stomp sends {chorus.enabled:false}
  act(() => result.current.togglePedal("chorus"));
  expect(mocked.patchChain).toHaveBeenCalledWith({ "chorus.enabled": false });
  // reverb has no engine enable: stomping it off pushes the wet send to 0
  act(() => result.current.togglePedal("reverb"));
  expect(mocked.patchParams).toHaveBeenCalledWith({ reverb: 0 });
});

test("soft-bypass keeps the stored value: the parked 0 echo is ignored, restore on re-enable", () => {
  const { result } = renderHook(() => usePolyclav());
  act(() =>
    handlers.snapshot?.({
      version: "",
      devices: { launchkey: "", xr18: "" },
      patches: [],
      params: { patch: "", reverb: 0.5 }, // -> reverb.mix 50
      player: null,
    }),
  );
  expect(result.current.state.chainValues["reverb.mix"]).toBe(50);
  // stomp reverb off -> engine wet send pushed to 0
  act(() => result.current.togglePedal("reverb"));
  expect(mocked.patchParams).toHaveBeenCalledWith({ reverb: 0 });
  // the daemon echoes {reverb:0}; while parked it must NOT clobber the stored 50
  act(() => handlers.params?.({ field: "reverb", value: 0 }));
  expect(result.current.state.chainValues["reverb.mix"]).toBe(50);
  // re-enable restores the stored value (50% -> 0.5)
  mocked.patchParams.mockClear();
  act(() => result.current.togglePedal("reverb"));
  expect(mocked.patchParams).toHaveBeenCalledWith({ reverb: 0.5 });
});

test("patch switch applies the daemon's nested-by-stage chain block", () => {
  const { result } = renderHook(() => usePolyclav());
  act(() =>
    handlers.patch?.({
      name: "Rhodes",
      chain: {
        chorus: { enabled: true, rate_hz: 1.2, depth: 0.5, mix: 0.3 },
        delay: { enabled: false, time_ms: 500, feedback: 0.4, mix: 0.2 },
      },
    }),
  );
  const s = result.current.state;
  expect(s.current).toBe("Rhodes");
  expect(s.chainValues["chorus.rate"]).toBeCloseTo(1.2);
  expect(s.chainValues["chorus.depth"]).toBe(50);
  expect(s.chainValues["chorus.mix"]).toBe(30);
  expect(s.chainValues["delay.time_ms"]).toBe(500);
  expect(s.chainValues["delay.feedback"]).toBe(40); // 0.4 engine -> 40%
  expect(s.enabled.chorus).toBe(true);
  expect(s.enabled.delay).toBe(false); // tremolo->trem / delay->delay stage mapping
});

test("a patch switch cancels a knob edit queued just before it", () => {
  vi.useFakeTimers();
  const { result } = renderHook(() => usePolyclav());
  act(() => result.current.setParam("delay.mix", 60)); // queued (90ms debounce)
  act(() => handlers.patch?.({ name: "Other" })); // switch before the flush fires
  act(() => vi.advanceTimersByTime(200));
  expect(mocked.patchChain).not.toHaveBeenCalled(); // the stale edit never lands
});

test("reorder writes the full FX order to the daemon (no localStorage)", () => {
  const { result } = renderHook(() => usePolyclav());
  const next = ["reverb", "drive", "chorus", "trem", "delay", "comp"];
  act(() => result.current.reorder(next));
  expect(result.current.state.order).toEqual(next);
  // all six pedals, engine stage ids (trem -> tremolo); nothing in localStorage.
  expect(mocked.patchChain).toHaveBeenCalledWith({
    order: ["reverb", "drive", "chorus", "tremolo", "delay", "comp"],
  });
  expect(window.localStorage.getItem("polyclav-pedal-order")).toBeNull();
});

test("a chain order SSE event reorders the board (engine ids -> pedal ids)", () => {
  const { result } = renderHook(() => usePolyclav());
  // The daemon's real shape: the array lives under `order`, not `value`.
  act(() =>
    handlers.chain?.({
      field: "order",
      order: ["reverb", "tremolo", "drive", "chorus", "delay", "comp"],
    }),
  );
  expect(result.current.state.order).toEqual([
    "reverb",
    "trem",
    "drive",
    "chorus",
    "delay",
    "comp",
  ]);
});
