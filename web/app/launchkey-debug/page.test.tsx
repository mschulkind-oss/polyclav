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
  afterEach(() => {
    Reflect.deleteProperty(navigator, "requestMIDIAccess");
    Reflect.deleteProperty(globalThis, "EventSource");
    FakeEventSource.instances = [];
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
    for (let i = 1; i <= 9; i++) {
      expect(container.querySelector(`[data-control="fader-${i}"]`)).toBeInTheDocument();
      expect(container.querySelector(`[data-control="fader-button-${i}"]`)).toBeInTheDocument();
    }
    for (let i = 1; i <= 8; i++) {
      expect(container.querySelector(`[data-control="encoder-${i}"]`)).toBeInTheDocument();
    }
    expect(container.querySelector('[data-control="play"]')).toBeInTheDocument();
    expect(container.querySelector('[data-control="track-left"]')).toBeInTheDocument();
    expect(screen.getByText("Recent raw input").closest("details")).not.toHaveAttribute("open");
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
    fireEvent.click(screen.getByText("Recent raw input"));
    expect(screen.getByText(/Launchkey MK4 61 MIDI In/)).toBeInTheDocument();
    expect(screen.getByText(/MIDI ch3 aftertouch unmapped/)).toBeInTheDocument();
    expect(screen.getByText(/d2 45/)).toBeInTheDocument();
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
