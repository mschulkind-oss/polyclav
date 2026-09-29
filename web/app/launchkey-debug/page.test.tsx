import { readFileSync } from "node:fs";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import LaunchkeyDebugPage from "./page";

type MIDIHandler = (event: { data: Uint8Array; timeStamp: number }) => void;

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  static CLOSED = 2;
  readyState = 1;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  listeners = new Map<string, (event: MessageEvent) => void>();
  close = vi.fn(() => {
    this.readyState = FakeEventSource.CLOSED;
  });

  constructor(public url: string) {
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, cb: EventListenerOrEventListenerObject) {
    this.listeners.set(type, cb as (event: MessageEvent) => void);
  }

  emit(type: string, data: unknown) {
    this.listeners.get(type)?.({ data: JSON.stringify(data) } as MessageEvent);
  }
}

function installMIDI() {
  const handlers = new Map<string, MIDIHandler>();
  const input = (name: string) => ({
    name,
    addEventListener: vi.fn((_type: string, cb: MIDIHandler) => handlers.set(name, cb)),
    removeEventListener: vi.fn(),
  });
  const midi = input("Launchkey MK4 61 MIDI In");
  const daw = input("Launchkey MK4 61 DAW In");
  const send = vi.fn();
  const access = {
    inputs: new Map([
      ["midi", midi],
      ["daw", daw],
    ]),
    outputs: { send },
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  };
  const request = vi.fn().mockResolvedValue(access);
  Object.defineProperty(navigator, "requestMIDIAccess", { configurable: true, value: request });
  return { access, handlers, request, send };
}

describe("Launchkey debugger", () => {
  it("overrides the dashboard's multi-column main grid for one full-width instrument", () => {
    const css = readFileSync("app/launchkey-debug/style.css", "utf8");
    expect(css).toMatch(/\.lk-debug\s*\{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\)/s);
    expect(css).toMatch(
      /\.lk-pad-row\s*\{[^}]*grid-template-columns:\s*repeat\(8,\s*minmax\(0,\s*1fr\)\)/s,
    );
  });

  it("keeps instructions legible on the dark surface", () => {
    const css = readFileSync("app/launchkey-debug/style.css", "utf8");
    expect(css).toMatch(/\.lk-panel\s*\{[^}]*color:\s*#[0-9a-f]{6}/s);
    expect(css).toMatch(/\.lk-body\s*\{[^}]*"mixer mixer"[^}]*"pads pads"/s);
  });

  afterEach(() => {
    Reflect.deleteProperty(navigator, "requestMIDIAccess");
    Reflect.deleteProperty(globalThis, "EventSource");
    FakeEventSource.instances = [];
  });

  it("does not draw native scroll arrows inside control tiles", () => {
    const css = readFileSync("app/launchkey-debug/style.css", "utf8");
    expect(css).not.toMatch(/\.lk-control small\s*\{[^}]*overflow-y:\s*auto/s);
    expect(css).toMatch(/\.lk-control small\s*\{[^}]*overflow:\s*hidden/s);
  });

  it("fits all pads and faders without sideways scrolling", () => {
    const css = readFileSync("app/launchkey-debug/style.css", "utf8");
    expect(css).not.toMatch(/min-width:\s*900px/);
    expect(css).toMatch(/\.lk-faders\s*\{[^}]*repeat\(9,\s*minmax\(0,\s*1fr\)\)/s);
    expect(css).not.toMatch(/\.lk-dense-scroll/);
    expect(css).toMatch(/@media \(max-width: 650px\)[\s\S]*?\.lk-body\s*\{/);
    expect(css).not.toMatch(/text-overflow:\s*ellipsis/);
  });

  it("renders source selector and a compact physical control surface", () => {
    const { container } = render(<LaunchkeyDebugPage />);
    expect(screen.getByRole("button", { name: "Browser Web MIDI" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "daemon raw SSE" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "offline report" })).toBeInTheDocument();
    expect(
      screen.getByRole("region", { name: "Launchkey 61 physical debug surface" }),
    ).toBeInTheDocument();
    expect(container.querySelectorAll(".lk-key")).toHaveLength(61);
    expect(container.querySelectorAll(".lk-pad-row [data-control]")).toHaveLength(16);
    expect(container.querySelectorAll(".lk-dense-scroll")).toHaveLength(0);
    expect(screen.getByText(/^Top row · 1–8$/)).toBeInTheDocument();
    expect(screen.getByText(/^Bottom row · 1–8$/)).toBeInTheDocument();
    for (let i = 1; i <= 9; i++) {
      expect(container.querySelector(`[data-control="fader-${i}"]`)).toBeInTheDocument();
      expect(container.querySelector(`[data-control="fader-button-${i}"]`)).toBeInTheDocument();
    }
    for (let i = 1; i <= 8; i++) {
      expect(container.querySelector(`[data-control="encoder-${i}"]`)).toBeInTheDocument();
    }
    expect(container.querySelector('[data-control="play"]')).toBeInTheDocument();
    expect(container.querySelector('[data-control="rewind"]')).not.toBeInTheDocument();
    expect(container.querySelector('[data-control="fast-forward"]')).not.toBeInTheDocument();
    expect(container.querySelector('[data-control="track-left"]')).toBeInTheDocument();
    expect(container.querySelector('[data-control="encoder-up"] span')).toHaveAttribute(
      "aria-label",
      "Encoder up",
    );
    expect(container.querySelector('[data-control="encoder-down"] span')).toHaveAttribute(
      "aria-label",
      "Encoder down",
    );
    expect(screen.getByText("Recent raw input").closest("details")).toHaveAttribute("open");
    expect(screen.getByText("Waiting for input")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveAttribute("tabindex", "0");
    const guidance = container.querySelector(".lk-source-note");
    expect(guidance).toHaveTextContent(/just web-dev.*localhost:3000/i);
    expect(guidance).toHaveTextContent(/just dev.*localhost:5100/i);
    expect(guidance).toHaveTextContent(/daemon.*DAW mode.*startup/i);
    expect(guidance).toHaveTextContent(/page cannot turn on DAW mode/i);
    expect(guidance).toHaveTextContent(/offline.*neither the daemon nor MIDI permission/i);
    expect(guidance).toHaveTextContent(/Settings.*Octave.*Fixed Chord.*no button MIDI/i);
    expect(container.querySelector('[data-control="fader-1"] span[role="img"]')).toHaveAttribute(
      "aria-label",
      "Fader 1",
    );
    expect(container.querySelector('[data-control="pad-top-1"] span')).toHaveAttribute(
      "aria-label",
      "Pad top 1",
    );
  });

  it("keeps the latest-input scroll region keyboard reachable when event text grows", () => {
    render(<LaunchkeyDebugPage />);
    const latest = screen.getByRole("status");
    latest.focus();
    expect(latest).toHaveFocus();
  });

  it("listens to both MIDI and DAW browser ports without opening outputs or sending messages", async () => {
    const { handlers, request, send } = installMIDI();
    const { container } = render(<LaunchkeyDebugPage />);
    fireEvent.click(screen.getByRole("button", { name: "Browser Web MIDI" }));
    await waitFor(() => expect(handlers.size).toBe(2));
    await act(async () => {
      handlers.get("Launchkey MK4 61 DAW In")?.({
        data: Uint8Array.of(0x90, 96, 100),
        timeStamp: 0,
      });
      handlers.get("Launchkey MK4 61 MIDI In")?.({
        data: Uint8Array.of(0x93, 60, 90),
        timeStamp: 0,
      });
    });
    expect(container.querySelector('[data-control="pad-top-1"]')).toHaveClass("held");
    expect(container.querySelector('[data-control="key-60"]')).toHaveClass("held");
    expect(request).toHaveBeenCalledWith({ sysex: false });
    expect(send).not.toHaveBeenCalled();
    expect(screen.getByRole("status")).toHaveTextContent(
      /Browser Web MIDI.*key 60.*note-on.*90.*MIDI/i,
    );
    expect(screen.getByRole("status")).toHaveTextContent(/channel 4/i);
  });

  it("does not reopen browser MIDI if permission resolves after switching away", async () => {
    let resolveAccess: (value: ReturnType<typeof installMIDI>["access"]) => void = () => {};
    const pending = new Promise<ReturnType<typeof installMIDI>["access"]>((resolve) => {
      resolveAccess = resolve;
    });
    const installed = installMIDI();
    installed.request.mockReturnValueOnce(pending);
    render(<LaunchkeyDebugPage />);

    fireEvent.click(screen.getByRole("button", { name: "Browser Web MIDI" }));
    fireEvent.click(screen.getByRole("button", { name: "offline report" }));
    await act(async () => resolveAccess(installed.access));

    expect(installed.handlers.size).toBe(0);
    expect(installed.access.addEventListener).not.toHaveBeenCalled();
    expect(screen.getByText(/Offline; load a report/)).toBeInTheDocument();
  });

  it("renders encoders as signed relative steps around center 64", async () => {
    const { handlers } = installMIDI();
    const { container } = render(<LaunchkeyDebugPage />);
    fireEvent.click(screen.getByRole("button", { name: "Browser Web MIDI" }));
    await waitFor(() => expect(handlers.size).toBe(2));
    await act(async () => {
      handlers.get("Launchkey MK4 61 DAW In")?.({
        data: Uint8Array.of(0xbf, 86, 66),
        timeStamp: 0,
      });
    });
    expect(container.querySelector('[data-control="encoder-2"]')).toHaveTextContent("step +2");
    expect(container.querySelector('[data-control="encoder-2"]')).not.toHaveTextContent(/speed/i);
  });

  it("shows fader and wheel positions but no invented value before input", async () => {
    const { handlers, send } = installMIDI();
    const { container } = render(<LaunchkeyDebugPage />);
    expect(
      container.querySelector('[data-control="fader-1"] .lk-fader-fill'),
    ).not.toBeInTheDocument();
    expect(
      container.querySelector('[data-control="pitch-wheel"] .lk-wheel-marker'),
    ).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Browser Web MIDI" }));
    await waitFor(() => expect(handlers.size).toBe(2));
    const daw = handlers.get("Launchkey MK4 61 DAW In");
    const midi = handlers.get("Launchkey MK4 61 MIDI In");
    await act(async () => {
      daw?.({ data: Uint8Array.of(0xbf, 5, 0), timeStamp: 0 });
      midi?.({ data: Uint8Array.of(0xe0, 0, 64), timeStamp: 0 });
      midi?.({ data: Uint8Array.of(0xb0, 1, 127), timeStamp: 0 });
    });
    expect(container.querySelector('[data-control="fader-1"] .lk-fader-fill')).toHaveStyle({
      height: "0%",
    });
    expect(container.querySelector('[data-control="pitch-wheel"] .lk-wheel-marker')).toHaveStyle({
      bottom: "50%",
    });
    expect(container.querySelector('[data-control="mod-wheel"] .lk-wheel-marker')).toHaveStyle({
      bottom: "100%",
    });
    expect(container.querySelector('[data-control="pitch-wheel"]')).toHaveTextContent("8192");
    await act(async () => daw?.({ data: Uint8Array.of(0xbf, 5, 127), timeStamp: 0 }));
    expect(container.querySelector('[data-control="fader-1"] .lk-fader-fill')).toHaveStyle({
      height: "100%",
    });
    expect(send).not.toHaveBeenCalled();
  });

  it("colors held pads only from measured aftertouch, clearing it on release", async () => {
    const { handlers } = installMIDI();
    const { container } = render(<LaunchkeyDebugPage />);
    fireEvent.click(screen.getByRole("button", { name: "Browser Web MIDI" }));
    await waitFor(() => expect(handlers.size).toBe(2));
    const daw = handlers.get("Launchkey MK4 61 DAW In");
    const pad = () => container.querySelector('[data-control="pad-top-1"]');
    await act(async () => daw?.({ data: Uint8Array.of(0x90, 96, 100), timeStamp: 0 }));
    expect(pad()).toHaveClass("held");
    expect(pad()).not.toHaveAttribute("data-pressure"); // velocity is not pressure
    await act(async () => daw?.({ data: Uint8Array.of(0xa0, 96, 110), timeStamp: 0 }));
    expect(pad()).toHaveAttribute("data-pressure", "110");
    expect(pad()).toHaveTextContent("pressure 110");
    await act(async () => daw?.({ data: Uint8Array.of(0x90, 96, 0), timeStamp: 0 }));
    expect(pad()).not.toHaveClass("held");
    expect(pad()).not.toHaveAttribute("data-pressure");
  });

  it("rotates an encoder marker by accumulated relative steps and resets on source change", async () => {
    const { handlers } = installMIDI();
    const { container } = render(<LaunchkeyDebugPage />);
    fireEvent.click(screen.getByRole("button", { name: "Browser Web MIDI" }));
    await waitFor(() => expect(handlers.size).toBe(2));
    const daw = handlers.get("Launchkey MK4 61 DAW In");
    const marker = () => container.querySelector('[data-control="encoder-1"] .lk-encoder-marker');
    await act(async () => daw?.({ data: Uint8Array.of(0xbf, 85, 66), timeStamp: 0 }));
    expect(marker()).toHaveStyle({ transform: "rotate(30deg)" });
    await act(async () => daw?.({ data: Uint8Array.of(0xbf, 85, 63), timeStamp: 0 }));
    expect(marker()).toHaveStyle({ transform: "rotate(15deg)" });
    expect(container.querySelector('[data-control="encoder-1"]')).toHaveTextContent("step -1");
    fireEvent.click(screen.getByRole("button", { name: "offline report" }));
    expect(marker()).not.toBeInTheDocument();
  });

  it("distinguishes an open SSE connection from an absent Launchkey", async () => {
    Object.defineProperty(globalThis, "EventSource", {
      configurable: true,
      value: FakeEventSource,
    });
    render(<LaunchkeyDebugPage />);
    fireEvent.click(screen.getByRole("button", { name: "daemon raw SSE" }));
    const es = await waitFor(() => FakeEventSource.instances[0]);
    act(() => es.onopen?.());
    act(() => es.emit("snapshot", { devices: { launchkey: "absent" } }));
    expect(screen.getByText(/SSE connected.*Launchkey absent/i)).toBeInTheDocument();
    expect(screen.getByText(/SSE connected.*Launchkey absent/i)).toHaveTextContent(
      /DAW controls will not arrive/i,
    );
  });

  it("shows daemon raw SSE events including unsupported input and full port identity", async () => {
    Object.defineProperty(globalThis, "EventSource", {
      configurable: true,
      value: FakeEventSource,
    });
    render(<LaunchkeyDebugPage />);
    fireEvent.click(screen.getByRole("button", { name: "daemon raw SSE" }));
    const es = await waitFor(() => FakeEventSource.instances[0]);
    act(() => es.onopen?.());
    act(() => {
      es.emit("midi-raw", {
        time: "2026-09-26T18:15:00.000Z",
        source: "performance",
        port: "Launchkey MK4 61 MIDI In",
        kind: "aftertouch",
        channel: 2,
        data1: 69,
        raw: "d245",
      });
    });
    expect(es.url).toBe("/api/events");
    expect(screen.getByRole("status")).toHaveTextContent(
      /daemon raw SSE.*unmapped.*aftertouch.*69.*Launchkey MK4 61 MIDI In.*channel 3/i,
    );
    expect(screen.getByText(/Launchkey MK4 61 MIDI In.*MIDI ch3/)).toBeInTheDocument();
    expect(screen.getByText(/MIDI ch3 aftertouch unmapped/)).toBeInTheDocument();
    expect(screen.getByText(/d2 45/)).toBeInTheDocument();
  });

  it("lights and releases the existing Track tiles for shifted Track CCs", async () => {
    Object.defineProperty(globalThis, "EventSource", {
      configurable: true,
      value: FakeEventSource,
    });
    const { container } = render(<LaunchkeyDebugPage />);
    fireEvent.click(screen.getByRole("button", { name: "daemon raw SSE" }));
    const es = await waitFor(() => FakeEventSource.instances[0]);
    const send = (cc: number, value: number) =>
      es.emit("midi-raw", {
        source: "launchkey-daw",
        port: "Launchkey MK4 61 DAW In",
        kind: "cc",
        channel: 0,
        data1: cc,
        data2: value,
        raw: `b0${cc.toString(16)}${value.toString(16)}`,
      });
    act(() => send(103, 127));
    expect(container.querySelector('[data-control="track-left"]')).toHaveClass("held");
    act(() => send(103, 0));
    expect(container.querySelector('[data-control="track-left"]')).not.toHaveClass("held");
    act(() => send(109, 127));
    expect(container.querySelector('[data-control="track-left"]')).toHaveClass("held");
    act(() => send(109, 0));
    expect(container.querySelector('[data-control="track-left"]')).not.toHaveClass("held");
    act(() => send(108, 127));
    expect(container.querySelector('[data-control="track-right"]')).toHaveClass("held");
    act(() => send(108, 0));
    expect(container.querySelector('[data-control="track-right"]')).not.toHaveClass("held");
  });

  it("clears green pads and keys for velocity-zero note-on over daemon SSE", async () => {
    Object.defineProperty(globalThis, "EventSource", {
      configurable: true,
      value: FakeEventSource,
    });
    const { container } = render(<LaunchkeyDebugPage />);
    fireEvent.click(screen.getByRole("button", { name: "daemon raw SSE" }));
    const es = await waitFor(() => FakeEventSource.instances[0]);
    const send = (source: string, port: string, note: number, velocity: number) => {
      es.emit("midi-raw", {
        source,
        port,
        kind: "note-on",
        channel: 0,
        data1: note,
        data2: velocity,
        raw: `90${note.toString(16)}${velocity.toString(16)}`,
      });
    };
    act(() => {
      send("launchkey-daw", "Launchkey MK4 61 DAW In", 96, 70);
      send("performance", "Launchkey MK4 61 MIDI In", 60, 70);
    });
    expect(container.querySelector('[data-control="pad-top-1"]')).toHaveClass("held");
    expect(container.querySelector('[data-control="key-60"]')).toHaveClass("held");
    act(() => {
      send("launchkey-daw", "Launchkey MK4 61 DAW In", 96, 0);
      send("performance", "Launchkey MK4 61 MIDI In", 60, 0);
    });
    expect(container.querySelector('[data-control="pad-top-1"]')).not.toHaveClass("held");
    expect(container.querySelector('[data-control="key-60"]')).not.toHaveClass("held");
  });

  it("ignores stale events from a previously selected source", async () => {
    const { handlers } = installMIDI();
    const { container } = render(<LaunchkeyDebugPage />);
    fireEvent.click(screen.getByRole("button", { name: "Browser Web MIDI" }));
    await waitFor(() => expect(handlers.size).toBe(2));
    fireEvent.click(screen.getByRole("button", { name: "offline report" }));
    await act(async () => {
      handlers.get("Launchkey MK4 61 DAW In")?.({
        data: Uint8Array.of(0x90, 96, 100),
        timeStamp: 0,
      });
    });
    expect(container.querySelector('[data-control="pad-top-1"]')).not.toHaveClass("held");
    expect(screen.getByText("Waiting for input")).toBeInTheDocument();
  });

  it("replays a local report and lights a press until release", async () => {
    const { container } = render(<LaunchkeyDebugPage />);
    const report = {
      controls: [
        {
          events: [
            {
              timestamp: "2026-09-26T18:15:00.000Z",
              port_role: "daw",
              kind: "note_on",
              channel: 1,
              raw_values: { note: 96, velocity: 99 },
            },
            {
              timestamp: "2026-09-26T18:15:00.100Z",
              port_role: "daw",
              kind: "note_off",
              channel: 1,
              raw_values: { note: 96 },
            },
          ],
        },
      ],
    };
    const file = new File([JSON.stringify(report)], "inventory.json", { type: "application/json" });
    Object.defineProperty(file, "text", { value: async () => JSON.stringify(report) });
    fireEvent.change(screen.getByLabelText("Load inventory JSON"), { target: { files: [file] } });
    const next = await screen.findByRole("button", { name: "Next event" });
    fireEvent.click(next);
    expect(container.querySelector('[data-control="pad-top-1"]')).toHaveClass("held");
    fireEvent.click(next);
    await waitFor(() =>
      expect(container.querySelector('[data-control="pad-top-1"]')).not.toHaveClass("held"),
    );
  });
});
